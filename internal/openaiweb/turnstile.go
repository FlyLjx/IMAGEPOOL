package openaiweb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The ChatGPT sentinel endpoint returns a compact instruction list in dx when
// it requests a Turnstile proof. This interpreter mirrors the web client's
// small instruction set without introducing a browser dependency.
type turnstileVM struct {
	values  map[turnstileMapKey]any
	started time.Time
	result  string
}

type turnstileFunction func([]any) error
type turnstileMapKey string

type turnstileOrderedMap struct {
	keys   []string
	values map[string]any
}

func newTurnstileOrderedMap() *turnstileOrderedMap {
	return &turnstileOrderedMap{values: map[string]any{}}
}

func (m *turnstileOrderedMap) set(key string, value any) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func solveTurnstileToken(dx, p string) (string, error) {
	raw, err := decodeTurnstileBase64(dx)
	if err != nil {
		return "", fmt.Errorf("decode turnstile challenge: %w", err)
	}
	program, err := decodeTurnstileJSON(xorTurnstileString(string(raw), p))
	if err != nil {
		return "", fmt.Errorf("decode turnstile program: %w", err)
	}
	instructions, ok := program.([]any)
	if !ok {
		return "", fmt.Errorf("turnstile program is not an instruction list")
	}
	vm := newTurnstileVM(instructions, p)
	failedInstructions := 0
	var lastInstructionErr error
	for index, instruction := range instructions {
		// The web implementation skips malformed individual instructions.
		if err := vm.execute(instruction); err != nil {
			failedInstructions++
			lastInstructionErr = fmt.Errorf("instruction %d: %w", index, err)
		}
	}
	if vm.result == "" {
		if lastInstructionErr != nil {
			return "", fmt.Errorf("turnstile program produced no token after %d skipped instructions: %w", failedInstructions, lastInstructionErr)
		}
		return "", fmt.Errorf("turnstile program produced no token")
	}
	return vm.result, nil
}

func newTurnstileVM(program []any, p string) *turnstileVM {
	vm := &turnstileVM{values: map[turnstileMapKey]any{}, started: time.Now()}
	vm.set(1, turnstileFunction(vm.opXOR))
	vm.set(2, turnstileFunction(vm.opAssign))
	vm.set(3, turnstileFunction(vm.opResult))
	vm.set(5, turnstileFunction(vm.opAppend))
	vm.set(6, turnstileFunction(vm.opPropertyWithLocation))
	vm.set(7, turnstileFunction(vm.opCallWithResolvedArgs))
	vm.set(8, turnstileFunction(vm.opCopy))
	vm.set(9, program)
	vm.set(10, "window")
	vm.set(14, turnstileFunction(vm.opJSONParse))
	vm.set(15, turnstileFunction(vm.opJSONStringify))
	vm.set(16, p)
	vm.set(17, turnstileFunction(vm.opConstruct))
	vm.set(18, turnstileFunction(vm.opBase64Decode))
	vm.set(19, turnstileFunction(vm.opBase64Encode))
	vm.set(20, turnstileFunction(vm.opConditionalCall))
	vm.set(21, turnstileFunction(func([]any) error { return nil }))
	vm.set(23, turnstileFunction(vm.opConditionalRawCall))
	vm.set(24, turnstileFunction(vm.opProperty))
	return vm
}

func (vm *turnstileVM) execute(raw any) error {
	instruction, ok := raw.([]any)
	if !ok || len(instruction) == 0 {
		return nil
	}
	opcode, ok := turnstileMapKeyFor(instruction[0])
	if !ok {
		return nil
	}
	target, ok := vm.values[opcode]
	if !ok {
		return nil
	}
	fn, ok := target.(turnstileFunction)
	if !ok {
		return nil
	}
	return fn(instruction[1:])
}

func (vm *turnstileVM) opXOR(args []any) error {
	leftKey, rightKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	left, err := vm.value(leftKey)
	if err != nil {
		return err
	}
	right, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	vm.values[leftKey] = xorTurnstileString(turnstileString(left), turnstileString(right))
	return nil
}

func (vm *turnstileVM) opAssign(args []any) error {
	if len(args) != 2 {
		return fmt.Errorf("turnstile assign has %d arguments", len(args))
	}
	key, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return fmt.Errorf("turnstile assign target is invalid")
	}
	vm.values[key] = args[1]
	return nil
}

func (vm *turnstileVM) opResult(args []any) error {
	if len(args) != 1 {
		return fmt.Errorf("turnstile result has %d arguments", len(args))
	}
	value, ok := args[0].(string)
	if !ok {
		return fmt.Errorf("turnstile result is not text")
	}
	vm.result = base64.StdEncoding.EncodeToString([]byte(value))
	return nil
}

func (vm *turnstileVM) opAppend(args []any) error {
	leftKey, rightKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	current, err := vm.value(leftKey)
	if err != nil {
		return err
	}
	incoming, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	if list, ok := current.([]any); ok {
		next := append([]any(nil), list...)
		vm.values[leftKey] = append(next, incoming)
		return nil
	}
	if turnstileStringOrFloat(current) || turnstileStringOrFloat(incoming) {
		vm.values[leftKey] = turnstileString(current) + turnstileString(incoming)
		return nil
	}
	vm.values[leftKey] = "NaN"
	return nil
}

func (vm *turnstileVM) opPropertyWithLocation(args []any) error {
	resultKey, leftKey, rightKey, err := turnstileThreeKeys(args)
	if err != nil {
		return err
	}
	left, err := vm.value(leftKey)
	if err != nil {
		return err
	}
	right, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	leftText, leftOK := left.(string)
	rightText, rightOK := right.(string)
	if !leftOK || !rightOK {
		return nil
	}
	value := leftText + "." + rightText
	if value == "window.document.location" {
		value = "https://chatgpt.com/"
	}
	vm.values[resultKey] = value
	return nil
}

func (vm *turnstileVM) opCallWithResolvedArgs(args []any) error {
	if len(args) < 1 {
		return fmt.Errorf("turnstile call has no target")
	}
	targetKey, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return fmt.Errorf("turnstile call target is invalid")
	}
	target, err := vm.value(targetKey)
	if err != nil {
		return err
	}
	values, err := vm.resolveValues(args[1:])
	if err != nil {
		return err
	}
	if targetText, ok := target.(string); ok && targetText == "window.Reflect.set" {
		if len(values) != 3 {
			return fmt.Errorf("turnstile Reflect.set has %d arguments", len(values))
		}
		object, ok := values[0].(*turnstileOrderedMap)
		if !ok {
			return fmt.Errorf("turnstile Reflect.set target is invalid")
		}
		object.set(pythonValueString(values[1]), values[2])
		return nil
	}
	return invokeTurnstileFunction(target, values)
}

func (vm *turnstileVM) opCopy(args []any) error {
	leftKey, rightKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	value, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	vm.values[leftKey] = value
	return nil
}

func (vm *turnstileVM) opJSONParse(args []any) error {
	leftKey, rightKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	encoded, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	text, ok := encoded.(string)
	if !ok {
		return fmt.Errorf("turnstile JSON.parse input is not text")
	}
	decoded, err := decodeTurnstileJSON(text)
	if err != nil {
		return err
	}
	vm.values[leftKey] = decoded
	return nil
}

func (vm *turnstileVM) opJSONStringify(args []any) error {
	leftKey, rightKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	value, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	encoded, err := marshalTurnstileJSON(value)
	if err != nil {
		return err
	}
	vm.values[leftKey] = encoded
	return nil
}

func (vm *turnstileVM) opConstruct(args []any) error {
	if len(args) < 2 {
		return fmt.Errorf("turnstile construct has %d arguments", len(args))
	}
	resultKey, targetKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	target, err := vm.value(targetKey)
	if err != nil {
		return err
	}
	values, err := vm.resolveValues(args[2:])
	if err != nil {
		return err
	}
	switch target {
	case "window.performance.now":
		vm.values[resultKey] = float64(time.Since(vm.started).Nanoseconds())/1e6 + rand.Float64()
	case "window.Object.create":
		vm.values[resultKey] = newTurnstileOrderedMap()
	case "window.Object.keys":
		if len(values) > 0 && values[0] == "window.localStorage" {
			vm.values[resultKey] = []any{
				"STATSIG_LOCAL_STORAGE_INTERNAL_STORE_V4",
				"STATSIG_LOCAL_STORAGE_STABLE_ID",
				"client-correlated-secret",
				"oai/apps/capExpiresAt",
				"oai-did",
				"STATSIG_LOCAL_STORAGE_LOGGING_REQUEST",
				"UiState.isNavigationCollapsed.1",
			}
		}
	case "window.Math.random":
		vm.values[resultKey] = rand.Float64()
	default:
		if err := invokeTurnstileFunction(target, values); err != nil {
			return err
		}
		vm.values[resultKey] = nil
	}
	return nil
}

func (vm *turnstileVM) opBase64Decode(args []any) error {
	if len(args) != 1 {
		return fmt.Errorf("turnstile base64 decode has %d arguments", len(args))
	}
	key, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return fmt.Errorf("turnstile base64 decode target is invalid (%T)", args[0])
	}
	value, err := vm.value(key)
	if err != nil {
		return err
	}
	decoded, err := decodeTurnstileBase64(turnstileString(value))
	if err != nil {
		return err
	}
	vm.values[key] = string(decoded)
	return nil
}

func (vm *turnstileVM) opBase64Encode(args []any) error {
	if len(args) != 1 {
		return fmt.Errorf("turnstile base64 encode has %d arguments", len(args))
	}
	key, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return fmt.Errorf("turnstile base64 encode target is invalid (%T)", args[0])
	}
	value, err := vm.value(key)
	if err != nil {
		return err
	}
	vm.values[key] = base64.StdEncoding.EncodeToString([]byte(turnstileString(value)))
	return nil
}

func (vm *turnstileVM) opConditionalCall(args []any) error {
	if len(args) < 3 {
		return fmt.Errorf("turnstile conditional call has %d arguments", len(args))
	}
	leftKey, rightKey, targetKey, err := turnstileThreeKeys(args)
	if err != nil {
		return err
	}
	left, err := vm.value(leftKey)
	if err != nil {
		return err
	}
	right, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(left, right) {
		return nil
	}
	target, err := vm.value(targetKey)
	if err != nil {
		return err
	}
	values, err := vm.resolveValues(args[3:])
	if err != nil {
		return err
	}
	return invokeTurnstileFunction(target, values)
}

func (vm *turnstileVM) opConditionalRawCall(args []any) error {
	if len(args) < 2 {
		return fmt.Errorf("turnstile raw call has %d arguments", len(args))
	}
	conditionKey, targetKey, err := turnstileTwoKeys(args)
	if err != nil {
		return err
	}
	condition, err := vm.value(conditionKey)
	if err != nil {
		return err
	}
	if condition == nil {
		return nil
	}
	target, err := vm.value(targetKey)
	if err != nil {
		return err
	}
	return invokeTurnstileFunction(target, args[2:])
}

func (vm *turnstileVM) opProperty(args []any) error {
	resultKey, leftKey, rightKey, err := turnstileThreeKeys(args)
	if err != nil {
		return err
	}
	left, err := vm.value(leftKey)
	if err != nil {
		return err
	}
	right, err := vm.value(rightKey)
	if err != nil {
		return err
	}
	leftText, leftOK := left.(string)
	rightText, rightOK := right.(string)
	if leftOK && rightOK {
		vm.values[resultKey] = leftText + "." + rightText
	}
	return nil
}

func (vm *turnstileVM) set(key any, value any) {
	if mapKey, ok := turnstileMapKeyFor(key); ok {
		vm.values[mapKey] = value
	}
}

func (vm *turnstileVM) value(key turnstileMapKey) (any, error) {
	value, ok := vm.values[key]
	if !ok {
		return nil, fmt.Errorf("turnstile map value is missing")
	}
	return value, nil
}

func (vm *turnstileVM) resolveValues(args []any) ([]any, error) {
	values := make([]any, 0, len(args))
	for _, arg := range args {
		key, ok := turnstileMapKeyFor(arg)
		if !ok {
			return nil, fmt.Errorf("turnstile argument is not a key")
		}
		value, err := vm.value(key)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func invokeTurnstileFunction(target any, args []any) error {
	fn, ok := target.(turnstileFunction)
	if !ok {
		return fmt.Errorf("turnstile target is not callable")
	}
	return fn(args)
}

func turnstileTwoKeys(args []any) (turnstileMapKey, turnstileMapKey, error) {
	if len(args) < 2 {
		return "", "", fmt.Errorf("turnstile operation has %d arguments", len(args))
	}
	left, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return "", "", fmt.Errorf("turnstile left key is invalid")
	}
	right, ok := turnstileMapKeyFor(args[1])
	if !ok {
		return "", "", fmt.Errorf("turnstile right key is invalid")
	}
	return left, right, nil
}

func turnstileThreeKeys(args []any) (turnstileMapKey, turnstileMapKey, turnstileMapKey, error) {
	if len(args) < 3 {
		return "", "", "", fmt.Errorf("turnstile operation has %d arguments", len(args))
	}
	first, ok := turnstileMapKeyFor(args[0])
	if !ok {
		return "", "", "", fmt.Errorf("turnstile first key is invalid")
	}
	second, ok := turnstileMapKeyFor(args[1])
	if !ok {
		return "", "", "", fmt.Errorf("turnstile second key is invalid")
	}
	third, ok := turnstileMapKeyFor(args[2])
	if !ok {
		return "", "", "", fmt.Errorf("turnstile third key is invalid")
	}
	return first, second, third, nil
}

func turnstileMapKeyFor(value any) (turnstileMapKey, bool) {
	switch typed := value.(type) {
	case nil:
		return "z:", true
	case string:
		return turnstileMapKey("s:" + typed), true
	case int:
		return turnstileIntegerKey(int64(typed)), true
	case int64:
		return turnstileIntegerKey(typed), true
	case float64:
		return turnstileFloatKey(typed), true
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
				return turnstileIntegerKey(parsed), true
			}
		}
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return turnstileFloatKey(parsed), true
		}
		return turnstileMapKey("n:" + text), true
	case bool:
		if typed {
			return turnstileIntegerKey(1), true
		}
		return turnstileIntegerKey(0), true
	default:
		return "", false
	}
}

func turnstileIntegerKey(value int64) turnstileMapKey {
	return turnstileMapKey("n:" + strconv.FormatInt(value, 10))
}

func turnstileFloatKey(value float64) turnstileMapKey {
	if value == 0 {
		return turnstileIntegerKey(0)
	}
	if math.Trunc(value) == value && value >= math.MinInt64 && value <= math.MaxInt64 {
		return turnstileIntegerKey(int64(value))
	}
	return turnstileMapKey("n:" + strconv.FormatFloat(value, 'g', -1, 64))
}

func turnstileStringOrFloat(value any) bool {
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func turnstileString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "undefined"
	case string:
		if replacement, ok := turnstileSpecialStrings[typed]; ok {
			return replacement
		}
		return typed
	case float64:
		return pythonFloatString(typed)
	case float32:
		return pythonFloatString(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return fmt.Sprint(value)
			}
			items = append(items, text)
		}
		return strings.Join(items, ",")
	case []string:
		return strings.Join(typed, ",")
	default:
		return fmt.Sprint(value)
	}
}

func pythonValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case float64:
		return pythonFloatString(typed)
	case float32:
		return pythonFloatString(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

var turnstileSpecialStrings = map[string]string{
	"window.Math":            "[object Math]",
	"window.Reflect":         "[object Reflect]",
	"window.performance":     "[object Performance]",
	"window.localStorage":    "[object Storage]",
	"window.Object":          "function Object() { [native code] }",
	"window.Reflect.set":     "function set() { [native code] }",
	"window.performance.now": "function () { [native code] }",
	"window.Object.create":   "function create() { [native code] }",
	"window.Object.keys":     "function keys() { [native code] }",
	"window.Math.random":     "function random() { [native code] }",
}

func pythonFloatString(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	case value == math.Trunc(value):
		return strconv.FormatFloat(value, 'f', 1, 64)
	default:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
}

func xorTurnstileString(text, key string) string {
	if key == "" {
		return text
	}
	textRunes := []rune(text)
	keyRunes := []rune(key)
	if len(keyRunes) == 0 {
		return text
	}
	for i := range textRunes {
		textRunes[i] ^= keyRunes[i%len(keyRunes)]
	}
	return string(textRunes)
}

func decodeTurnstileBase64(value string) ([]byte, error) {
	encoded := strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		return decoded, nil
	}
	encoded = strings.TrimRight(encoded, "=")
	if decoded, err := base64.RawStdEncoding.DecodeString(encoded); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(encoded)
}

func decodeTurnstileJSON(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	return decodeTurnstileJSONValue(decoder)
}

func decodeTurnstileJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := newTurnstileOrderedMap()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not text")
				}
				value, err := decodeTurnstileJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object.set(key, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("object was not terminated")
			}
			return object, nil
		case '[':
			list := []any{}
			for decoder.More() {
				value, err := decodeTurnstileJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("array was not terminated")
			}
			return list, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", typed)
		}
	case json.Number:
		return normalizeTurnstileNumber(typed), nil
	default:
		return typed, nil
	}
}

func normalizeTurnstileNumber(number json.Number) any {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if value, err := strconv.ParseInt(text, 10, 64); err == nil {
			return value
		}
		return number
	}
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return value
	}
	return number
}

func marshalTurnstileJSON(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "null", nil
	case string:
		return quoteTurnstileJSONString(typed), nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case json.Number:
		return typed.String(), nil
	case float64:
		return pythonJSONFloat(typed), nil
	case float32:
		return pythonJSONFloat(float64(typed)), nil
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			encoded, err := marshalTurnstileJSON(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, encoded)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []string:
		items := make([]any, len(typed))
		for i := range typed {
			items[i] = typed[i]
		}
		return marshalTurnstileJSON(items)
	case *turnstileOrderedMap:
		parts := make([]string, 0, len(typed.keys))
		for _, key := range typed.keys {
			encoded, err := marshalTurnstileJSON(typed.values[key])
			if err != nil {
				return "", err
			}
			parts = append(parts, quoteTurnstileJSONString(key)+": "+encoded)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		object := newTurnstileOrderedMap()
		for _, key := range keys {
			object.set(key, typed[key])
		}
		return marshalTurnstileJSON(object)
	default:
		return "", fmt.Errorf("cannot JSON encode turnstile value %T", value)
	}
}

func pythonJSONFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return pythonFloatString(value)
	}
}

func quoteTurnstileJSONString(value string) string {
	var out strings.Builder
	out.Grow(len(value) + 2)
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			out.WriteString("\\\\")
		case '"':
			out.WriteString("\\\"")
		case '\b':
			out.WriteString("\\b")
		case '\f':
			out.WriteString("\\f")
		case '\n':
			out.WriteString("\\n")
		case '\r':
			out.WriteString("\\r")
		case '\t':
			out.WriteString("\\t")
		default:
			if r < 0x20 || r > 0x7e {
				if r <= 0xffff {
					_, _ = fmt.Fprintf(&out, "\\u%04x", r)
				} else {
					code := r - 0x10000
					_, _ = fmt.Fprintf(&out, "\\u%04x\\u%04x", 0xd800+(code>>10), 0xdc00+(code&0x3ff))
				}
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}
