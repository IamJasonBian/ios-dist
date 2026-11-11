// Command ascreg registers iPhone UDIDs with the App Store Connect team, using the
// ASC API key in ~/.appstoreconnect (issuer_id + private_keys/AuthKey_<kid>.p8).
//
//	ascreg -list                 print registered iOS devices (read-only; checks the key)
//	ascreg <udid>=<name> ...     register each; an already-registered UDID counts as success
//	ascreg -rename <udid>=<name> ...  rename registered devices (display name only)
//
// Exits non-zero if any registration failed.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const api = "https://api.appstoreconnect.apple.com/v1/devices"

func main() {
	list := flag.Bool("list", false, "list registered iOS devices")
	rename := flag.Bool("rename", false, "rename registered devices: <udid>=<name> ...")
	flag.Parse()
	tok, err := token()
	if err != nil {
		fail(err)
	}
	if *list {
		body, code := call(tok, "GET", api+"?filter[platform]=IOS&limit=200&fields[devices]=name,udid,status", nil)
		if code != 200 {
			fail(fmt.Errorf("list: HTTP %d %s", code, body))
		}
		var r struct {
			Data []struct{ Attributes map[string]string }
		}
		json.Unmarshal(body, &r)
		for _, d := range r.Data {
			fmt.Printf("%s\t%s\t%s\n", d.Attributes["udid"], d.Attributes["status"], d.Attributes["name"])
		}
		return
	}
	if *rename {
		body, code := call(tok, "GET", api+"?filter[platform]=IOS&limit=200&fields[devices]=udid", nil)
		if code != 200 {
			fail(fmt.Errorf("list: HTTP %d %s", code, body))
		}
		var r struct {
			Data []struct {
				ID         string
				Attributes map[string]string
			}
		}
		json.Unmarshal(body, &r)
		ids := map[string]string{}
		for _, d := range r.Data {
			ids[d.Attributes["udid"]] = d.ID
		}
		bad := 0
		for _, arg := range flag.Args() {
			udid, name, _ := strings.Cut(arg, "=")
			id := ids[udid]
			if id == "" || name == "" {
				bad++
				fmt.Fprintf(os.Stderr, "FAILED %s: not registered or no name\n", udid)
				continue
			}
			req, _ := json.Marshal(map[string]any{"data": map[string]any{"type": "devices", "id": id,
				"attributes": map[string]string{"name": name}}})
			if b, c := call(tok, "PATCH", api+"/"+id, req); c != 200 {
				bad++
				fmt.Fprintf(os.Stderr, "FAILED %s: HTTP %d %s\n", udid, c, b)
			} else {
				fmt.Printf("renamed %s → %s\n", udid, name)
			}
		}
		if bad > 0 {
			os.Exit(1)
		}
		return
	}
	bad := 0
	for _, arg := range flag.Args() {
		udid, name, _ := strings.Cut(arg, "=")
		if name == "" {
			name = "taildrop " + udid[len(udid)-6:]
		}
		req, _ := json.Marshal(map[string]any{"data": map[string]any{"type": "devices",
			"attributes": map[string]string{"name": name, "platform": "IOS", "udid": udid}}})
		body, code := call(tok, "POST", api, req)
		switch {
		case code == 201:
			fmt.Printf("registered %s (%s)\n", udid, name)
		case code == 409 && bytes.Contains(bytes.ToLower(body), []byte("already exists")):
			fmt.Printf("already registered %s\n", udid)
		default:
			bad++
			fmt.Fprintf(os.Stderr, "FAILED %s: HTTP %d %s\n", udid, code, body)
		}
	}
	if bad > 0 {
		os.Exit(1)
	}
}

func call(tok, method, url string, body []byte) ([]byte, int) {
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return []byte(err.Error()), 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

// token builds a 10-minute ES256 JWT for the ASC API from the local key.
func token() (string, error) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".appstoreconnect")
	issuer, err := os.ReadFile(filepath.Join(dir, "issuer_id"))
	if err != nil {
		return "", err
	}
	keys, _ := filepath.Glob(filepath.Join(dir, "private_keys", "AuthKey_*.p8"))
	if len(keys) == 0 {
		return "", fmt.Errorf("no AuthKey_*.p8 in %s/private_keys", dir)
	}
	kid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(keys[0]), "AuthKey_"), ".p8")
	pemBytes, _ := os.ReadFile(keys[0])
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return "", fmt.Errorf("bad PEM in %s", keys[0])
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return "", err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("%s is not an EC key", keys[0])
	}
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": kid, "typ": "JWT"})
	now := time.Now().Unix()
	claims, _ := json.Marshal(map[string]any{"iss": strings.TrimSpace(string(issuer)), "iat": now,
		"exp": now + 600, "aud": "appstoreconnect-v1"})
	msg := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(msg))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return msg + "." + enc.EncodeToString(sig), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ascreg:", err)
	os.Exit(1)
}
