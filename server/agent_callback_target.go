package main

import (
	"errors"
	"net/url"
	"strings"
)

// agentCallbackTarget validates callback transport and reconciles explicit
// login/issuer and redirect targets. A browser-selected ID is not authorization.
func agentCallbackTarget(raw, target string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return "", "", errors.New("invalid callback_url")
	}
	host := strings.ToLower(u.Hostname())
	local := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if host == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(raw, "\\\r\n\t") || (u.Scheme != "https" && !(local && u.Scheme == "http")) {
		return "", "", errors.New("callback_url must use HTTPS for non-localhost addresses")
	}
	q := u.Query()
	ids := q["agent_id"]
	if len(ids) > 1 {
		return "", "", errors.New("ambiguous callback target")
	}
	carried := strings.TrimSpace(q.Get("agent_id"))
	target = strings.TrimSpace(target)
	if target != "" && carried != "" && target != carried {
		return "", "", errors.New("callback target mismatch")
	}
	if target == "" {
		target = carried
	}
	if target == "" {
		return "", "", errors.New("agent_id required")
	}
	q.Set("agent_id", target)
	u.RawQuery = q.Encode()
	return u.String(), target, nil
}