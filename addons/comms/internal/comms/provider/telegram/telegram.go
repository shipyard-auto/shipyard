// Package telegram implements the comms provider for Telegram bots over
// the official Bot API (https://core.telegram.org/bots/api). One channel
// corresponds to one bot token, which Telegram associates with a single
// bot identity.
//
// v1 supports only text messages to a single recipient via sendMessage.
// Media, polls and keyboards are explicitly out of scope.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms/provider"
)

// defaultAPIBase is the public Telegram Bot API root. Overridable on the
// Provider struct for tests (httptest.Server) and for self-hosted
// deployments behind a Bot API proxy.
const defaultAPIBase = "https://api.telegram.org"

// maxResponseBytes caps how much of the API response the provider reads
// into memory. Telegram replies are typically <1 KiB; 64 KiB is generous
// without exposing the addon to a memory exhaustion attack via a
// misbehaving (or spoofed) endpoint.
const maxResponseBytes int64 = 64 * 1024

// secret is the credential schema for Telegram channels. It is the JSON
// the SecretStore returns from ~/.shipyard/comms/secrets/<id>.json.
type secret struct {
	Token string `json:"token"`
}

// HTTPClient mirrors *http.Client.Do for injection in tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Provider implements provider.Provider for Telegram. Construct via New
// for production defaults, or instantiate the struct directly in tests
// to swap APIBase / HTTPClient / Now.
type Provider struct {
	HTTPClient HTTPClient
	APIBase    string // empty → defaultAPIBase
	Now        func() time.Time
}

// New returns a Provider with a 30s HTTP timeout and the public Bot API
// endpoint. The timeout covers slow networks but is short enough that a
// hung send fails fast rather than blocking the caller for minutes.
func New() *Provider {
	return &Provider{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		APIBase:    defaultAPIBase,
		Now:        time.Now,
	}
}

// Name returns "telegram" — matches comms.ChannelTypeTelegram.
func (p *Provider) Name() string { return "telegram" }

// Send delivers msg.Body to msg.To[0].Raw using the bot identified by
// secret.token. Validates the inputs strictly: empty token, multiple
// recipients, empty body and non-text-only payloads are rejected before
// any network call.
func (p *Provider) Send(ctx context.Context, _ comms.Channel, raw []byte, msg comms.Message) (provider.Receipt, error) {
	if len(raw) == 0 {
		return provider.Receipt{}, errors.New("telegram: empty secret (run 'shipyard comms channel add --type telegram --token ...')")
	}
	var sec secret
	if err := json.Unmarshal(raw, &sec); err != nil {
		return provider.Receipt{}, fmt.Errorf("telegram: parse secret: %w", err)
	}
	if sec.Token == "" {
		return provider.Receipt{}, errors.New("telegram: secret.token is required")
	}
	if len(msg.To) != 1 {
		return provider.Receipt{}, fmt.Errorf("telegram: send accepts exactly 1 recipient, got %d", len(msg.To))
	}
	to := msg.To[0]
	if to.Raw == "" {
		return provider.Receipt{}, errors.New("telegram: to.raw (chat_id or @username) is required")
	}
	if msg.Body == "" {
		return provider.Receipt{}, errors.New("telegram: message body is required (parts/media not supported in v1)")
	}

	apiBase := p.APIBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(apiBase, "/"), sec.Token)

	form := url.Values{}
	form.Set("chat_id", to.Raw)
	form.Set("text", msg.Body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return provider.Receipt{}, fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := p.HTTPClient
	if client == nil {
		return provider.Receipt{}, errors.New("telegram: HTTPClient is nil (construct via telegram.New)")
	}
	resp, err := client.Do(req)
	if err != nil {
		return provider.Receipt{}, fmt.Errorf("telegram: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return provider.Receipt{}, fmt.Errorf("telegram: read response: %w", err)
	}

	var apiResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		// Surface HTTP status + first 256 bytes of body so the user has
		// enough to diagnose without a debug build.
		snippet := string(body)
		if len(snippet) > 256 {
			snippet = snippet[:256] + "…"
		}
		return provider.Receipt{}, fmt.Errorf("telegram: parse response (http=%d body=%q): %w", resp.StatusCode, snippet, err)
	}
	if !apiResp.OK {
		return provider.Receipt{}, fmt.Errorf("telegram: api error %d: %s", apiResp.ErrorCode, apiResp.Description)
	}

	now := p.Now
	if now == nil {
		now = time.Now
	}
	return provider.Receipt{
		ProviderMessageID: fmt.Sprintf("%d", apiResp.Result.MessageID),
		SentAt:            now().UTC(),
	}, nil
}
