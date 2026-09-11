package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/andriotisnikos1/dit/internal/apiclient"
	"github.com/andriotisnikos1/dit/internal/apitypes"
)

// ErrNonInteractive reports that a prompt was needed but prompting is disabled.
var ErrNonInteractive = errors.New("a prompt is required but the CLI is running non-interactively")

// canPrompt reports whether the CLI may ask the operator something.
func (a *App) canPrompt() bool {
	if a.NonInteractive {
		return false
	}
	return stdinIsTerminal()
}

// promptLine asks for a visible value.
func (a *App) promptLine(label string) (string, error) {
	if !a.canPrompt() {
		return "", ErrNonInteractive
	}
	fmt.Fprintf(a.Stderr, "%s: ", label)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptSecret asks for a value without echoing it.
func (a *App) promptSecret(label string) (string, error) {
	if !a.canPrompt() {
		return "", ErrNonInteractive
	}
	fmt.Fprintf(a.Stderr, "%s: ", label)
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(a.Stderr)
	if err != nil {
		return "", fmt.Errorf("read hidden input: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// readPasswordStdin reads a password from stdin, for --password-stdin. This is
// how scripts and CI supply secrets without putting them in the process list.
func readPasswordStdin() (string, error) {
	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("--password-stdin requires a password on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return "", errors.New("--password-stdin received an empty password")
	}
	return password, nil
}

// resolvePassword returns the password from --password-stdin or a hidden
// prompt, and the username from the flag or a prompt.
func (a *App) resolveCredentials(registry, username string, passwordStdin bool) (string, string, error) {
	password := ""
	if passwordStdin {
		var err error
		if password, err = readPasswordStdin(); err != nil {
			return "", "", err
		}
	} else if a.canPrompt() {
		fmt.Fprintf(a.Stderr, "Credentials for %s\n", registry)
		var err error
		if password, err = a.promptSecret("Password"); err != nil {
			return "", "", err
		}
	} else {
		return "", "", fmt.Errorf("%w: pass --password-stdin, or run `dit creds set %s`",
			ErrNonInteractive, registry)
	}

	if strings.TrimSpace(username) == "" && a.canPrompt() {
		line, err := a.promptLine("Username")
		if err != nil {
			return "", "", err
		}
		username = line
	}
	if strings.TrimSpace(password) == "" {
		return "", "", errors.New("password must not be empty")
	}
	return strings.TrimSpace(username), password, nil
}

// promptAndStoreCredentials implements the 428 flow: the server says it needs
// credentials, the CLI collects them, stores them and lets the caller retry.
//
// It returns the registry the credentials were stored for.
func (a *App) promptAndStoreCredentials(registry string) (string, error) {
	host := strings.TrimSpace(registry)
	if host == "" {
		return "", errors.New("the server reported that credentials are required but named no registry")
	}
	if !a.canPrompt() {
		return "", fmt.Errorf(
			"%w: registry %s needs credentials; run `dit creds set %s` (or pass --non-interactive off) and retry",
			ErrNonInteractive, host, host)
	}

	fmt.Fprintf(a.Stderr, "\nRegistry %s requires authentication.\n", host)
	username, err := a.promptLine("Username")
	if err != nil {
		return "", err
	}
	password, err := a.promptSecret("Password")
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("password must not be empty")
	}

	ctx, cancel := a.Context()
	defer cancel()

	if _, err := a.Client.PutCredentials(ctx, host, apitypes.CredentialsRequest{
		Username: username,
		Password: password,
		Kind:     apitypes.CredentialBasic,
	}); err != nil {
		return "", fmt.Errorf("store credentials for %s: %w", host, err)
	}
	fmt.Fprintf(a.Stderr, "Stored credentials for %s.\n", host)
	return host, nil
}

// isCredentialsRequired reports whether err is the server's 428 trigger.
func isCredentialsRequired(err error) bool {
	return apiclient.IsCredentialsRequired(err)
}
