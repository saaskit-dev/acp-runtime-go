package openai

func unsupportedParameter(param string) *requestValidationError {
	return &requestValidationError{param: param, message: param + " is not supported by this text-only ACP gateway"}
}

// Neutral OpenAI sampling defaults are accepted for SDK compatibility. Provider
// sampling remains provider-controlled; non-default settings cannot be honored.
func validateSampling(temperature, topP *float64) *requestValidationError {
	if temperature != nil && *temperature != 1 {
		return unsupportedParameter("temperature")
	}
	if topP != nil && *topP != 1 {
		return unsupportedParameter("top_p")
	}
	return nil
}

func emptyStop(stop any) bool {
	if stop == nil {
		return true
	}
	switch values := stop.(type) {
	case []any:
		return len(values) == 0
	case []string:
		return len(values) == 0
	default:
		return false
	}
}
