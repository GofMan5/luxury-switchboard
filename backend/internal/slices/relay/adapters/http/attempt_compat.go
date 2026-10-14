package relayhttp

import (
	"net/http"
	"time"

	relayapp "github.com/luxuryprivate/switchboard/backend/internal/slices/relay/application"
)

// prepareLocalAttempt always starts with the client's normalized payload. A
// provider's repairs and wire dialect never become the next provider's input.
func (server *Server) prepareLocalAttempt(client *http.Request, original []byte, route relayapp.Route) (path string, body []byte, image, chat, responses, stateless bool, headers http.Header, err error) {
	path, body = client.URL.Path, original
	contentType := client.Header.Get("Content-Type")
	stream := requiresStreamTerminal(client, original)
	path, body, image, err = prepareImageRequest(client.Method, path, body, contentType, route.UpstreamModel, route.ImageCompat)
	if err != nil {
		return
	}
	chatMode := route.Format == "chat" || (route.Format == "auto" && server.chatOnlyLoaded(route.ProviderID))
	path, body, chat, err = prepareChatCompletions(client.Method, path, body, contentType, route.ChatPath, chatMode)
	if err != nil {
		return
	}
	canonical := canonicalPath(client.URL.Path)
	responses = !image && route.Format == "responses" && chatDialectPath(canonical)
	stateless = !image && route.ResponsesStateless && responsesDialectPath(canonical) && !stream
	if responses {
		body, err = chatRequestToResponses(body, route.ResponsesStateless)
		path = route.ResponsesPath
		if path == "" {
			path = responsesPath
		}
	} else if route.ResponsesStateless && !image && !chat && responsesDialectPath(canonical) {
		body, err = shapeStatelessResponsesRequest(body)
	}
	if err != nil {
		return
	}
	if !image && route.UpstreamModel != "" {
		body = rewriteRequestModel(body, contentType, route.UpstreamModel)
	}
	if !image && !chat && !responses && !stateless {
		body = ensureStreamUsage(body, contentType, client.URL.Path)
	}
	headers = client.Header.Clone()
	if route.CacheTTL >= time.Hour {
		var carries bool
		body, carries = extendCacheTTL(body, contentType)
		if carries {
			declareBetaFeature(headers, extendedCacheTTLBeta)
		}
	}
	return
}
