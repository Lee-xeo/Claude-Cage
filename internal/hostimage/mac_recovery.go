package hostimage

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Apple Internet Recovery's public protocol and EFI verification key are
// documented by OpenCorePkg/Utilities/macrecovery (BSD-3-Clause).
const appleRecoveryRSA = "C3E748CAD9CD384329E10E25A91E43E1A762FF529ADE578C935BDDF9B13F2179D4855E6FC89E9E29CA12517D17DFA1EDCE0BEBF0EA7B461FFE61D94E2BDF72C196F89ACD3536B644064014DAE25A15DB6BB0852ECBD120916318D1CCDEA3C84C92ED743FC176D0BACA920D3FCF3158AFF731F88CE0623182A8ED67E650515F75745909F07D415F55FC15A35654D118C55A462D37A3ACDA08612F3F3F6571761EFCCBCC299AEE99B3A4FD6212CCFFF5EF37A2C334E871191F7E1C31960E010A54E86FA3F62E6D6905E1CD57732410A3EB0C6B4DEFDABE9F59BF1618758C751CD56CEF851D1C0EAA1C558E37AC108DA9089863D20E2E7E4BF475EC66FE6B3EFDCF"

func verifyRecoverySignature(data []byte) error {
	if len(data) < 292 || data[10] != 1 {
		return errors.New("Apple recovery image has no digital signature")
	}
	end := binary.LittleEndian.Uint64(data[28:36])
	if end > uint64(len(data)) || end+256 != uint64(len(data)) {
		return errors.New("invalid Apple recovery signature")
	}
	signature := append([]byte{}, data[end:]...)
	for left, right := 0, len(signature)-1; left < right; left, right = left+1, right-1 {
		signature[left], signature[right] = signature[right], signature[left]
	}
	modulus, ok := new(big.Int).SetString(appleRecoveryRSA, 16)
	if !ok {
		return errors.New("invalid Apple recovery trust anchor")
	}
	digest := sha256.Sum256(data[:end])
	return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: modulus, E: 65537}, crypto.SHA256, digest[:], signature)
}

func PrepareIntelRecovery(ctx context.Context, cache, version string, transport http.RoundTripper) (string, error) {
	major := strings.Split(version, ".")[0]
	image, manifest := filepath.Join(cache, "BaseSystem-"+major+".dmg"), filepath.Join(cache, "BaseSystem-"+major+".chunklist")
	verify := func() error {
		data, err := os.ReadFile(manifest)
		if err != nil {
			return err
		}
		if err := verifyRecoverySignature(data); err != nil {
			return err
		}
		return verifyIntelChunks(ctx, image, data)
	}
	if regularFile(image) && regularFile(manifest) {
		if err := verify(); err != nil {
			return "", err
		}
		return image, nil
	}
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Host != via[0].URL.Host || (via[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
			return errors.New("untrusted Apple recovery redirect")
		}
		return validateRecoveryURL(req.URL)
	}}
	request := func(address, method, cookie string, body io.Reader) (*http.Response, error) {
		u, err := url.Parse(address)
		if err != nil {
			return nil, err
		}
		if err := validateRecoveryURL(u); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "InternetRecovery/1.0")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if body != nil {
			req.Header.Set("Content-Type", "text/plain")
		}
		return client.Do(req)
	}
	response, err := request("https://osrecovery.apple.com/", "GET", "", nil)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	session := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == "session" {
			session = cookie.Name + "=" + cookie.Value
			break
		}
	}
	if session == "" {
		return "", errors.New("Apple recovery session unavailable")
	}
	nonce := func(n int) string {
		data := make([]byte, n)
		_, _ = rand.Read(data)
		return strings.ToUpper(hex.EncodeToString(data))
	}
	board := "Mac-7BA5B2D9E42DDD94"
	if major == "26" {
		board = "Mac-27AD2F918AE68F61"
	}
	body := "cid=" + nonce(8) + "\nsn=00000000000000000\nbid=" + board + "\nk=" + nonce(32) + "\nfg=" + nonce(32) + "\nos=latest"
	response, err = request("https://osrecovery.apple.com/InstallationPayload/RecoveryImage", "POST", session, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	response.Body.Close()
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if ok {
			values[key] = value
		}
	}
	if values["AU"] == "" || values["CU"] == "" || values["AT"] == "" || values["CT"] == "" {
		return "", errors.New("Apple recovery catalog did not return complete media")
	}
	response, err = request(values["CU"], "GET", "AssetToken="+values["CT"], nil)
	if err != nil {
		return "", err
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	response.Body.Close()
	if err != nil {
		return "", err
	}
	if err := verifyRecoverySignature(data); err != nil {
		return "", err
	}
	if len(data) < 36 {
		return "", errors.New("invalid Apple recovery manifest")
	}
	count := binary.LittleEndian.Uint64(data[12:20])
	if count > 1<<16 || 36+count*36 > uint64(len(data)) {
		return "", errors.New("invalid Apple recovery chunk count")
	}
	var size int64
	for index := uint64(0); index < count; index++ {
		size += int64(binary.LittleEndian.Uint32(data[36+36*index : 40+36*index]))
	}
	if size <= 0 || size > 4<<30 {
		return "", errors.New("invalid Apple recovery image size")
	}
	partial := image + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := info.Size()
	if offset > size {
		if err := file.Truncate(0); err != nil {
			return "", err
		}
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	failures := 0
	for offset < size {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		end := offset + (1 << 20) - 1
		if end >= size {
			end = size - 1
		}
		uri, err := url.Parse(values["AU"])
		if err != nil {
			return "", err
		}
		query := uri.Query()
		query.Set("range_start", strconv.FormatInt(offset, 10))
		query.Set("range_end", strconv.FormatInt(end, 10))
		query.Set("request", nonce(8))
		uri.RawQuery = query.Encode()
		if err := validateRecoveryURL(uri); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", uri.String(), nil)
		if err != nil {
			return "", err
		}
		if uri.Host != "oscdn.apple.com" && uri.Host != "swcdn.apple.com" && uri.Host != "updates.cdn-apple.com" {
			return "", errors.New("untrusted Apple recovery CDN")
		}
		req.Header.Set("User-Agent", "InternetRecovery/1.0")
		req.Header.Set("Cookie", "AssetToken="+values["AT"])
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
		req.Header.Set("Accept-Encoding", "identity")
		res, err := client.Do(req)
		if err != nil {
			failures++
			if failures >= 5 || ctx.Err() != nil {
				return "", err
			}
			continue
		}
		var start, finish, total int64
		_, parseErr := fmt.Sscanf(res.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &finish, &total)
		if res.StatusCode != 206 || parseErr != nil || start != offset || finish != end || total != size {
			res.Body.Close()
			return "", errors.New("Apple recovery download range mismatch")
		}
		n, copyErr := io.CopyN(file, res.Body, end-offset+1)
		res.Body.Close()
		offset += n
		if copyErr != nil {
			failures++
			if failures >= 5 || ctx.Err() != nil {
				return "", copyErr
			}
			continue
		}
		failures = 0
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := verifyIntelChunks(ctx, partial, data); err != nil {
		return "", err
	}
	if err := os.Rename(partial, image); err != nil {
		return "", err
	}
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		return "", err
	}
	return image, nil
}

func validateRecoveryURL(u *url.URL) error {
	if u.User != nil || u.Port() != "" || u.Fragment != "" {
		return errors.New("untrusted Apple recovery address")
	}
	if u.Host == "osrecovery.apple.com" && u.Scheme == "https" {
		return nil
	}
	// Apple's Recovery service returns HTTP CDN URLs. Authenticity comes from
	// the EFI RSA signature and every SHA-256 chunk, before any image is used.
	// Preserve the supplied scheme: upgrading oscdn to HTTPS breaks some CDNs.
	if (u.Scheme == "https" || u.Scheme == "http") && (u.Host == "oscdn.apple.com" || u.Host == "swcdn.apple.com" || u.Host == "updates.cdn-apple.com") {
		return nil
	}
	return errors.New("untrusted Apple recovery service")
}
