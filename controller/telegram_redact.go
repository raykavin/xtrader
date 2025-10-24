package controller

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// redactedToken stands for the token of the Telegram bot in the lines logged.
const redactedToken = "[redacted]"

var (
	// botPathPattern matches the token in a URL of the Telegram API, whatever
	// the token: /bot<token>/<method> and /file/bot<token>/<path>.
	botPathPattern = regexp.MustCompile(`/bot[^/\s"]+`)

	// botTokenPattern matches the token of a Telegram bot anywhere: the ID of
	// the bot, a colon and its secret, eg:
	// 123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11.
	botTokenPattern = regexp.MustCompile(`\d+:[\w-]{30,}`)
)

// redactToken returns text without the token of the bot: as it is, escaped as
// in a URL or quoted as in an error of net/url, and any token in a path of the
// Telegram API or looking like one. telebot calls the API on
// <api>/bot<token>/<method>, so the errors of its requests, eg: a *url.Error
// of net/http, carry the token.
func redactToken(text, token string) string {
	if token != "" {
		quoted := strconv.Quote(token)
		forms := []string{token, quoted[1 : len(quoted)-1], (&url.URL{Path: token}).EscapedPath()}
		// the longest first, where two match at the same place, eg: %25 and
		// its escaped form %2525
		slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
		pairs := make([]string, 0, 2*len(forms))
		for _, form := range forms {
			pairs = append(pairs, form, redactedToken)
		}
		text = strings.NewReplacer(pairs...).Replace(text)
	}
	text = botPathPattern.ReplaceAllString(text, "/bot"+redactedToken)
	return botTokenPattern.ReplaceAllString(text, redactedToken)
}

// redactError returns err without the token of the bot, see redactToken: an
// error with its message only, nil for nil.
func redactError(err error, token string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactToken(err.Error(), token))
}

// redact returns err without the token of the bot, see redactToken: every
// error of telebot is logged through it.
func (t *Telegram) redact(err error) error {
	return redactError(err, t.token)
}

// report is the Reporter of telebot, which would log the token otherwise: it
// logs an error of the poller, eg: the API out of reach, or the panic of a
// handler, then holds the poller for pollRetry: telebot polls again at once
// after an error, which floods the logs and the API.
func (t *Telegram) report(err error) {
	log.Errorf("telegram: the updates: %v", t.redact(err))
	select {
	case <-t.ctx.Done():
	case <-time.After(pollRetry):
	}
}
