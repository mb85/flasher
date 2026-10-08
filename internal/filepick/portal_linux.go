package filepick

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// open asks xdg-desktop-portal for a file, which shows the desktop's own
// file dialog (KDE's on Plasma, GTK's on GNOME).
func open(title string) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()

	tok := make([]byte, 8)
	rand.Read(tok)
	token := "flasher_" + hex.EncodeToString(tok)
	// The portal derives the request path from our bus name and the token,
	// so subscribe before calling to avoid missing a fast response.
	sender := strings.ReplaceAll(strings.TrimPrefix(conn.Names()[0], ":"), ".", "_")
	reqPath := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(reqPath),
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	type filter struct {
		Name     string
		Patterns []struct {
			Kind    uint32
			Pattern string
		}
	}
	mk := func(name string, globs ...string) filter {
		f := filter{Name: name}
		for _, g := range globs {
			f.Patterns = append(f.Patterns, struct {
				Kind    uint32
				Pattern string
			}{0, g})
		}
		return f
	}
	images := mk("Disk images", Patterns...)
	all := mk("All files", "*")
	opts := map[string]dbus.Variant{
		"handle_token":   dbus.MakeVariant(token),
		"modal":          dbus.MakeVariant(true),
		"filters":        dbus.MakeVariant([]filter{images, all}),
		"current_filter": dbus.MakeVariant(images),
	}
	var handle dbus.ObjectPath
	err = conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop").
		Call("org.freedesktop.portal.FileChooser.OpenFile", 0, "", title, opts).Store(&handle)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	timeout := time.After(time.Hour)
	for {
		select {
		case sig := <-signals:
			if sig.Path != handle && sig.Path != reqPath || len(sig.Body) < 2 {
				continue
			}
			if code, _ := sig.Body[0].(uint32); code != 0 {
				return "", ErrCancelled
			}
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			uris, _ := results["uris"].Value().([]string)
			if len(uris) == 0 {
				return "", ErrCancelled
			}
			u, err := url.Parse(uris[0])
			if err != nil || u.Scheme != "file" {
				return "", fmt.Errorf("the portal returned %q, which is not a local file", uris[0])
			}
			return u.Path, nil
		case <-timeout:
			return "", errors.New("timed out waiting for the file dialog")
		}
	}
}
