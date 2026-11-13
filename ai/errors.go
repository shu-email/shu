package ai

import "strconv"

// Kind says what went wrong, so the UI can explain it in the user's
// language.
type Kind int

// The failures of the AI layer.
const (
	// KindDisabled, KindFeatureOff, KindNoKey and KindNoModel stop a
	// request before any network call: AI is not set up for it.
	KindDisabled Kind = iota + 1
	KindFeatureOff
	KindNoKey
	KindNoModel
	// KindBadConfig is an endpoint that is not a usable URL, or an unknown
	// provider.
	KindBadConfig
	// KindInsecure is a remote server over plain HTTP without the user's
	// opt-in.
	KindInsecure
	KindInvalidKey
	KindRateLimit
	KindTimeout
	KindUnreachable
	KindUnsupportedModel
	// KindServer is any other answer the provider gave with an error
	// status.
	KindServer
	// KindBadResponse is an answer that could not be read.
	KindBadResponse
	KindCancelled
	// KindEmpty is a message with nothing left to send after the excerpt.
	KindEmpty
	KindKeychain
	// Local command-line providers: not installed, a path that does not
	// run, signed out, a failed launch, too much output.
	KindCLINotInstalled
	KindCLIBadPath
	KindCLISignIn
	KindCLIFailed
	KindCLIOutput
	// KindProviderOff is a request from a provider the user switched off.
	KindProviderOff
	// KindKeyEndpoint is a stored key given to another endpoint than the
	// one the request would go to now.
	KindKeyEndpoint
)

// Error is every failure this package returns. It carries the kind and
// the HTTP status only: never the prompt, mail text, an address or the
// key, so it is safe to show and to log.
type Error struct {
	Kind   Kind
	Status int
}

func (e *Error) Error() string {
	text := "ai: "
	switch e.Kind {
	case KindDisabled:
		text += "AI features are turned off"
	case KindFeatureOff:
		text += "this AI feature is turned off"
	case KindNoKey:
		text += "no API key is stored for the provider"
	case KindNoModel:
		text += "no model is chosen"
	case KindBadConfig:
		text += "the provider settings are not valid"
	case KindInsecure:
		text += "the server uses plain HTTP, which is not allowed"
	case KindInvalidKey:
		text += "the provider rejected the API key"
	case KindRateLimit:
		text += "the provider's rate limit was reached"
	case KindTimeout:
		text += "the request timed out"
	case KindUnreachable:
		text += "the server could not be reached"
	case KindUnsupportedModel:
		text += "the provider does not offer this model"
	case KindServer:
		text += "the provider answered with an error"
	case KindBadResponse:
		text += "the model's answer could not be read"
	case KindCancelled:
		text += "the request was cancelled"
	case KindEmpty:
		text += "the message has no text to send"
	case KindCLINotInstalled:
		return "The local tool is not installed."
	case KindCLIBadPath:
		return "The command path does not run. Check it in Settings."
	case KindCLISignIn:
		return "Sign in to the local tool first."
	case KindCLIFailed:
		return "The local tool failed. Check that it is signed in and up to date."
	case KindCLIOutput:
		return "The local tool returned too much text."
	case KindProviderOff:
		return "This provider is switched off. Turn it on in Settings."
	case KindKeyEndpoint:
		return "This API key was saved for another server. Save it again for this one."
	case KindKeychain:
		text += "the system keychain is not available"
	default:
		text += "unknown error"
	}
	if e.Status != 0 {
		text += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	return text
}

// KindOf returns the kind of an error from this package, or 0.
func KindOf(err error) Kind {
	for err != nil {
		if e, ok := err.(*Error); ok {
			return e.Kind
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return 0
		}
		err = u.Unwrap()
	}
	return 0
}
