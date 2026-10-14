package relayhttp

// finalAnswerTerminal reads the client dialect after compatibility conversion.
// An upstream chat 'done' is not evidence that a converted Responses answer
// completed: length/content_filter are incomplete in that dialect.
func finalAnswerTerminal(body []byte, path string) string {
	if !bodyLooksLikeEventStream(body) {
		return jsonTerminal(body)
	}
	inspector := sseInspector{path: canonicalPath(path)}
	inspector.Feed(body)
	terminal := inspector.Finish()
	// A real policy refusal stays distinct; a converted content_filter
	// incomplete remains incomplete just as its final client envelope says.
	if inspector.refused && terminal != "response.incomplete" && terminal != "response.invalid" {
		return terminalRefused
	}
	switch terminal {
	case "response.failed", "response.incomplete", "response.invalid":
		return terminal
	}
	return ""
}

func terminalActivityDetail(terminal string, responseBody []byte, secrets []string, requestBody []byte) (string, string) {
	code, detail := "", ""
	switch terminal {
	case terminalRefused:
		code, detail = "policy_refusal", "Provider response was refused"
	case "response.incomplete", "response.invalid":
		code, detail = "stream_incomplete", "Provider stream ended incomplete"
	case "response.failed":
		code, detail = "upstream_status", "Provider reported a failed response"
	}
	extracted := terminalErrorDetailUnbounded(responseBody, secrets)
	if extracted != "" {
		detail = truncateErrorDetail(redactRequestEchoes(extracted, requestBody))
	}
	return code, detail
}
