package openaiweb

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagepool/internal/accounts"
)

const turnstileVMTimeout = 75 * time.Second

//go:embed turnstile_vm.mjs
var embeddedTurnstileVMScript []byte

var turnstileVMScriptFile struct {
	sync.Mutex
	path string
	err  error
}

type turnstileVMOutput struct {
	OK           bool   `json:"ok"`
	Turnstile    string `json:"turnstile"`
	DecodedError string `json:"turnstile_result_decoded_if_error"`
	GenericError string `json:"error"`
}

func (c *Client) resolveTurnstileToken(ctx context.Context, account accounts.Account, dx, p string) (string, error) {
	if token, err := solveTurnstileToken(dx, p); err == nil {
		return token, nil
	}
	return c.runTurnstileVM(ctx, account, dx, p)
}

func (c *Client) runTurnstileVM(ctx context.Context, account accounts.Account, dx, p string) (string, error) {
	if strings.TrimSpace(dx) == "" {
		return "", fmt.Errorf("turnstile challenge has no dx program")
	}
	script, err := turnstileVMScriptPath()
	if err != nil {
		return "", err
	}
	deviceID := strings.TrimSpace(account.DeviceID)
	if deviceID == "" && account.FP != nil {
		deviceID = strings.TrimSpace(account.FP["oai-device-id"])
	}
	if deviceID == "" {
		if c != nil && c.newID != nil {
			deviceID = c.newID()
		} else {
			deviceID = newUUID()
		}
	}
	payload, err := json.Marshal(map[string]string{
		"p":            p,
		"turnstile_dx": dx,
		"device_id":    deviceID,
		"flow":         "chat_requirements",
		"user_agent":   c.userAgent(account),
		"href":         strings.TrimRight(c.baseURL, "/") + "/",
	})
	if err != nil {
		return "", fmt.Errorf("encode turnstile VM input: %w", err)
	}
	timeout := turnstileVMTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := strings.TrimSpace(os.Getenv("IMAGE_POOL_TURNSTILE_VM_COMMAND"))
	if command == "" {
		command = "node"
	}
	cmd := exec.CommandContext(runCtx, command, script)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return "", fmt.Errorf("turnstile VM timed out after %s", timeout.Round(time.Second))
		}
		return "", fmt.Errorf("turnstile VM execution failed: %s", conciseTurnstileVMError(stderr.String(), err.Error()))
	}
	return parseTurnstileVMOutput(stdout.Bytes())
}

func turnstileVMScriptPath() (string, error) {
	turnstileVMScriptFile.Lock()
	defer turnstileVMScriptFile.Unlock()
	if turnstileVMScriptFile.path != "" || turnstileVMScriptFile.err != nil {
		return turnstileVMScriptFile.path, turnstileVMScriptFile.err
	}
	directory, err := os.MkdirTemp("", "image-pool-turnstile-vm-")
	if err != nil {
		turnstileVMScriptFile.err = fmt.Errorf("create turnstile VM directory: %w", err)
		return "", turnstileVMScriptFile.err
	}
	path := filepath.Join(directory, "turnstile_vm.mjs")
	if err := os.WriteFile(path, embeddedTurnstileVMScript, 0o600); err != nil {
		_ = os.RemoveAll(directory)
		turnstileVMScriptFile.err = fmt.Errorf("write turnstile VM script: %w", err)
		return "", turnstileVMScriptFile.err
	}
	turnstileVMScriptFile.path = path
	return path, nil
}

func parseTurnstileVMOutput(data []byte) (string, error) {
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 || len(lines[len(lines)-1]) == 0 {
		return "", fmt.Errorf("turnstile VM produced no output")
	}
	var output turnstileVMOutput
	if err := json.Unmarshal(lines[len(lines)-1], &output); err != nil {
		return "", fmt.Errorf("decode turnstile VM output: %w", err)
	}
	if output.OK && strings.TrimSpace(output.Turnstile) != "" {
		return strings.TrimSpace(output.Turnstile), nil
	}
	detail := conciseTurnstileVMError(output.DecodedError, output.GenericError)
	if detail == "" {
		detail = "no token returned"
	}
	return "", fmt.Errorf("turnstile VM failed: %s", detail)
}

func conciseTurnstileVMError(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > 600 {
			value = value[len(value)-600:]
		}
		return value
	}
	return ""
}
