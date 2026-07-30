package adobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"imagepool/internal/config"
	proxyservice "imagepool/internal/proxy"
)

const (
	adobe2APIKey   = "projectx_webapp"
	uploadURL      = "https://firefly-3p.ff.adobe.io/v2/storage/image"
	imageSubmitURL = "https://firefly-3p.ff.adobe.io/v2/3p-images/generate-async"
)

type ImageReference struct {
	Data     []byte
	MIMEType string
}

type ImageGenerateRequest struct {
	Prompt       string
	Model        string
	Size         string
	AspectRatio  string
	Quality      string
	OutputFormat string
	References   []ImageReference
	Progress     func(progress string, percent int, details map[string]any)
}

type ImageGenerateResult struct {
	Images        [][]byte
	Model         string
	UpstreamJobID string
}

type upstreamError struct {
	Kind       string
	StatusCode int
	Message    string
	Retryable  bool
	Ambiguous  bool
}

func (e *upstreamError) Error() string { return e.Message }

func (r *Runtime) GenerateImage(ctx context.Context, request ImageGenerateRequest) (ImageGenerateResult, error) {
	model, err := validateImageGenerateRequest(&request)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	deadlineCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		deadlineCtx, cancel = context.WithTimeout(ctx, time.Duration(r.config.GenerateTimeoutSecs)*time.Second)
		defer cancel()
	}
	excluded := make([]string, 0)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		session, err := r.repository.SelectAccountSession(deadlineCtx, excluded, "")
		if err != nil {
			if lastErr != nil {
				return ImageGenerateResult{}, lastErr
			}
			return ImageGenerateResult{}, err
		}
		excluded = append(excluded, session.Account.AccountID)
		result, generateErr := r.generateImageWithSession(deadlineCtx, session, model, request)
		_ = r.repository.RecordGenerationResult(context.Background(), session.Account.AccountID, generateErr)
		if generateErr == nil {
			r.refreshCreditsAfterGeneration(session.Account.AccountID)
			return result, nil
		}
		lastErr = generateErr
		var upstream *upstreamError
		if !errors.As(generateErr, &upstream) || !upstream.Retryable || upstream.Ambiguous {
			return ImageGenerateResult{}, generateErr
		}
	}
	return ImageGenerateResult{}, lastErr
}

func (r *Runtime) GenerateImageWithAccount(ctx context.Context, accountID string, request ImageGenerateRequest) (ImageGenerateResult, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ImageGenerateResult{}, errors.New("account_id is required")
	}
	model, err := validateImageGenerateRequest(&request)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	deadlineCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		deadlineCtx, cancel = context.WithTimeout(ctx, time.Duration(r.config.GenerateTimeoutSecs)*time.Second)
		defer cancel()
	}
	session, err := r.repository.SelectAccountSession(deadlineCtx, nil, accountID)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	result, generateErr := r.generateImageWithSession(deadlineCtx, session, model, request)
	_ = r.repository.RecordGenerationResult(context.Background(), session.Account.AccountID, generateErr)
	if generateErr == nil {
		r.refreshCreditsAfterGeneration(session.Account.AccountID)
	}
	return result, generateErr
}

func validateImageGenerateRequest(request *ImageGenerateRequest) (Model, error) {
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return Model{}, errors.New("prompt is required")
	}
	if len([]rune(request.Prompt)) > 1200 {
		return Model{}, errors.New("prompt must be at most 1200 characters")
	}
	if len(request.References) > 6 {
		return Model{}, errors.New("Adobe image models support at most 6 reference images")
	}
	outputFormat, err := normalizeAdobeOutputFormat(request.OutputFormat)
	if err != nil {
		return Model{}, err
	}
	request.OutputFormat = outputFormat
	model, err := ResolveRequestedModelWithSize(request.Model, request.AspectRatio, request.Size)
	if err != nil {
		return Model{}, err
	}
	request.Model = model.ID
	return model, nil
}

func normalizeAdobeOutputFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), ".")) {
	case "", "auto", "png":
		return "png", nil
	case "jpg", "jpeg":
		return "jpeg", nil
	case "webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("invalid Adobe output_format %q; supported values are png, jpeg, webp", value)
	}
}

func adobeOutputMIMEType(outputFormat string) string {
	if outputFormat == "jpeg" {
		return "image/jpeg"
	}
	return "image/" + outputFormat
}

func (r *Runtime) generateImageWithSession(ctx context.Context, account AccountSession, model Model, request ImageGenerateRequest) (ImageGenerateResult, error) {
	client, err := r.httpClientForAccount(ctx, account.Account)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	session := account.Snapshot()
	sourceIDs := make([]string, 0, len(request.References))
	for index, reference := range request.References {
		if len(reference.Data) == 0 || len(reference.Data) > 10<<20 {
			return ImageGenerateResult{}, fmt.Errorf("reference image %d must be between 1 byte and 10 MiB", index+1)
		}
		mimeType := strings.ToLower(strings.TrimSpace(reference.MIMEType))
		if mimeType != "image/jpeg" && mimeType != "image/png" && mimeType != "image/webp" {
			return ImageGenerateResult{}, fmt.Errorf("reference image %d has unsupported MIME type %q", index+1, mimeType)
		}
		if request.Progress != nil {
			request.Progress("uploading", 10, map[string]any{"index": index + 1})
		}
		id, err := uploadReference(ctx, client, session, reference)
		if err != nil {
			return ImageGenerateResult{}, err
		}
		sourceIDs = append(sourceIDs, id)
	}
	payload, err := imagePayload(model, request.Prompt, request.Quality, sourceIDs)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	if request.Progress != nil {
		request.Progress("starting_generation", 25, nil)
	}
	// Express 3P models currently emit PNG regardless of the requested MIME.
	// The image service applies the client-requested format after download.
	pollURL, jobID, err := submitImage(ctx, client, session, request.Prompt, "png", payload)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	if request.Progress != nil {
		request.Progress("polling_image", 40, map[string]any{"upstream_job_id": jobID})
	}
	resultURL, err := r.pollImage(ctx, client, session, pollURL, jobID, request.Progress)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	if request.Progress != nil {
		request.Progress("downloading_image", 90, map[string]any{"upstream_job_id": jobID})
	}
	image, err := downloadGeneratedImage(ctx, client, resultURL)
	if err != nil {
		return ImageGenerateResult{}, err
	}
	return ImageGenerateResult{Images: [][]byte{image}, Model: model.ID, UpstreamJobID: jobID}, nil
}

func (r *Runtime) httpClientForAccount(ctx context.Context, account Account) (*http.Client, error) {
	routeURL, err := r.repository.RouteProxyURL(ctx, account.RouteAffinity)
	if err != nil {
		return nil, err
	}
	runtime := adobeRouteProxyRuntime(routeURL)
	return proxyservice.NewHTTPClientForRuntime(runtime, 60*time.Second)
}

func adobeRouteProxyRuntime(routeURL string) config.ProxyRuntime {
	if IsDirectRouteURL(routeURL) {
		return config.ProxyRuntime{Enabled: true, EgressMode: "direct"}
	}
	return config.ProxyRuntime{Enabled: true, EgressMode: "single_proxy", ProxyURL: routeURL, ResourceProxyURL: routeURL}
}

func uploadReference(ctx context.Context, client *http.Client, session SessionSnapshot, reference ImageReference) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(reference.Data))
	if err != nil {
		return "", err
	}
	applyAdobeHeaders(req, session)
	req.Header.Set("Content-Type", reference.MIMEType)
	response, err := client.Do(req)
	if err != nil {
		return "", &upstreamError{Kind: "network", Message: "Adobe reference upload failed", Ambiguous: true}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", classifyAdobeHTTPError("reference upload", response, body)
	}
	var payload struct {
		Images []struct {
			ID string `json:"id"`
		} `json:"images"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Images) == 0 || strings.TrimSpace(payload.Images[0].ID) == "" {
		return "", errors.New("Adobe reference upload returned no image id")
	}
	return payload.Images[0].ID, nil
}

func submitImage(ctx context.Context, client *http.Client, session SessionSnapshot, prompt, outputFormat string, payload map[string]any) (string, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, imageSubmitURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	applyAdobeSubmitHeaders(req, session, prompt, outputFormat)
	response, err := client.Do(req)
	if err != nil {
		return "", "", &upstreamError{Kind: "network", Message: "Adobe image submission result is unknown", Ambiguous: true}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", classifyAdobeHTTPError("image submission", response, responseBody)
	}
	var result map[string]any
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", "", errors.New("Adobe image submission returned invalid JSON")
	}
	pollURL := strings.TrimSpace(response.Header.Get("x-override-status-link"))
	if pollURL == "" {
		pollURL = resultLink(result)
	}
	if pollURL == "" {
		return "", "", errors.New("Adobe image submission returned no polling URL")
	}
	return pollURL, jobIDFromURL(pollURL), nil
}

func applyAdobeSubmitHeaders(req *http.Request, session SessionSnapshot, prompt, outputFormat string) {
	applyAdobeHeaders(req, session)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-accept-mimetype", adobeOutputMIMEType(outputFormat))
	accountID, _ := AccountIDFromToken(session.AccessToken)
	prefix := []rune(prompt)
	if len(prefix) > 256 {
		prefix = prefix[:256]
	}
	digest := sha256.Sum256([]byte(accountID + "-" + string(prefix)))
	req.Header.Set("x-nonce", hex.EncodeToString(digest[:]))
	req.Header.Set("priority", "u=1, i")
}

func (r *Runtime) pollImage(ctx context.Context, client *http.Client, session SessionSnapshot, pollURL, jobID string, progress func(string, int, map[string]any)) (string, error) {
	ticker := time.NewTicker(2500 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+session.AccessToken)
		req.Header.Set("User-Agent", session.UserAgent)
		response, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 2<<20))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				var payload map[string]any
				if json.Unmarshal(body, &payload) == nil {
					if resultURL := imageResultURL(payload); resultURL != "" {
						return resultURL, nil
					}
					if terminalAdobeFailure(payload, response.Header.Get("x-task-status")) {
						return "", &upstreamError{Kind: "upstream", Message: "Adobe image generation failed"}
					}
				}
			} else if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
				return "", classifyAdobeHTTPError("image polling", response, body)
			}
		}
		if progress != nil {
			progress("polling_image", 70, map[string]any{"upstream_job_id": jobID})
		}
		select {
		case <-ctx.Done():
			return "", &upstreamError{Kind: "timeout", Message: "Adobe image polling timed out"}
		case <-ticker.C:
		}
	}
}

func downloadGeneratedImage(ctx context.Context, client *http.Client, resultURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("download Adobe generated image failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download Adobe generated image returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 30<<20))
	if err != nil || len(data) == 0 {
		return nil, errors.New("downloaded Adobe generated image is empty or unreadable")
	}
	return data, nil
}

func applyAdobeHeaders(req *http.Request, session SessionSnapshot) {
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("x-api-key", adobe2APIKey)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://new.express.adobe.com")
	req.Header.Set("Referer", "https://new.express.adobe.com/")
	req.Header.Set("User-Agent", session.UserAgent)
	req.Header.Set("sec-ch-ua", session.SecCHUA)
	req.Header.Set("Accept-Language", session.AcceptLanguage)
}

func classifyAdobeHTTPError(operation string, response *http.Response, body []byte) error {
	text := strings.ToLower(string(body))
	status := response.StatusCode
	if status == http.StatusRequestTimeout || strings.Contains(text, "timeout_error") {
		return &upstreamError{Kind: "timeout", StatusCode: status, Message: operation + " timed out", Retryable: true}
	}
	if (status == http.StatusForbidden || status == http.StatusUnauthorized) && (strings.EqualFold(response.Header.Get("x-access-error"), "taste_exhausted") || strings.Contains(text, "taste_exhausted")) {
		return &upstreamError{Kind: "quota_exhausted", StatusCode: status, Message: "Adobe account quota exhausted", Retryable: true}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &upstreamError{Kind: "auth_invalid", StatusCode: status, Message: "Adobe account authentication failed", Retryable: true}
	}
	if status == http.StatusTooManyRequests || status == http.StatusUnavailableForLegalReasons || status >= 500 {
		return &upstreamError{Kind: "upstream_temporary", StatusCode: status, Message: fmt.Sprintf("%s temporarily failed with HTTP %d", operation, status), Retryable: true}
	}
	if status == http.StatusBadRequest || status == http.StatusUnprocessableEntity {
		return &upstreamError{Kind: "content_rejected", StatusCode: status, Message: "Adobe rejected the image request"}
	}
	return &upstreamError{Kind: "upstream", StatusCode: status, Message: fmt.Sprintf("%s failed with HTTP %d", operation, status)}
}

func resultLink(payload map[string]any) string {
	links, _ := payload["links"].(map[string]any)
	switch result := links["result"].(type) {
	case string:
		return strings.TrimSpace(result)
	case map[string]any:
		return strings.TrimSpace(fmt.Sprint(result["href"]))
	}
	return ""
}

func imageResultURL(payload map[string]any) string {
	outputs, _ := payload["outputs"].([]any)
	if len(outputs) == 0 {
		return ""
	}
	output, _ := outputs[0].(map[string]any)
	image, _ := output["image"].(map[string]any)
	for _, key := range []string{"presignedUrl", "presigned_url", "url"} {
		if value := strings.TrimSpace(fmt.Sprint(image[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func terminalAdobeFailure(payload map[string]any, header string) bool {
	status := strings.ToUpper(strings.TrimSpace(header))
	if status == "FAILED" || status == "ERROR" || status == "CANCELED" {
		return true
	}
	status = strings.ToUpper(strings.TrimSpace(fmt.Sprint(payload["status"])))
	return status == "FAILED" || status == "ERROR" || status == "CANCELED"
}

func jobIDFromURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}
