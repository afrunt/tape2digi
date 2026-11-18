// Package i18n holds the backend message catalogs (English and Ukrainian) and
// the current locale.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

const (
	English   = "en"
	Ukrainian = "uk"
)

var locale atomic.Value

func init() { locale.Store(English) }

// Valid reports whether lang is a supported language code.
func Valid(lang string) bool { return lang == English || lang == Ukrainian }

// Normalize maps an empty or unknown code to English.
func Normalize(lang string) string {
	if Valid(lang) {
		return lang
	}
	return English
}

// SetLocale switches the active language; an unknown code selects English.
func SetLocale(lang string) { locale.Store(Normalize(lang)) }

// Locale returns the active language code.
func Locale() string { return locale.Load().(string) }

// Key marks a format argument that is itself a catalog key; it is translated
// when the message is formatted.
type Key string

func catalog(lang string) map[string]string {
	if lang == Ukrainian {
		return uk
	}
	return en
}

func lookup(lang, key string) string {
	if m, ok := catalog(lang)[key]; ok {
		return m
	}
	if m, ok := en[key]; ok {
		return m
	}
	return key
}

func format(lang, key string, args []any) string {
	tmpl := strings.ReplaceAll(lookup(lang, key), "%w", "%v")
	if len(args) == 0 {
		return tmpl
	}
	out := make([]any, len(args))
	for i, a := range args {
		if k, ok := a.(Key); ok {
			out[i] = lookup(lang, string(k))
		} else {
			out[i] = a
		}
	}
	return fmt.Sprintf(tmpl, out...)
}

// T returns the message for key in the active language, formatted with args.
// A missing entry falls back to English and then to the key itself.
func T(key string, args ...any) string { return format(Locale(), key, args) }

type msgError struct {
	key  string
	args []any
}

func (e *msgError) Error() string { return T(e.key, e.args...) }

// Unwrap exposes the first error argument so errors.Is and errors.As see it.
func (e *msgError) Unwrap() error {
	for _, a := range e.args {
		if err, ok := a.(error); ok {
			return err
		}
	}
	return nil
}

// New returns an error whose text is resolved in the active language each time
// Error is called, so package-level sentinels follow a later language switch.
func New(key string, args ...any) error { return &msgError{key: key, args: args} }
