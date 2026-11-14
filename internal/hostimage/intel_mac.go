package hostimage

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lima-vm/lima/v2/pkg/plist"
	"golang.org/x/mod/semver"
)

func intelMacVersionCompatible(host, guest string) bool {
	h, g := "v"+host, "v"+guest
	return semver.IsValid(h) && semver.IsValid(g) && semver.Major(h) == semver.Major(g) &&
		semver.Compare(g, h) >= 0 && semver.Compare(h, "v15.4") >= 0 && semver.Compare(g, "v15.4") >= 0
}

const intelMacCatalogURL = "https://swscan.apple.com/content/catalogs/others/index-26-15-14-13-12-10.16-10.15-10.14-10.13-10.12-10.11-10.10-10.9-mountainlion-lion-snowleopard-leopard.merged-1.sucatalog.gz"

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func appleMetadata(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Host != "swscan.apple.com" && u.Host != "swdist.apple.com" && u.Host != "swcdn.apple.com") {
		return nil, errors.New("untrusted Apple catalog address")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Apple catalog returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("Apple catalog exceeds its size limit")
	}
	return data, nil
}

func intelMacCatalog(ctx context.Context, client *http.Client, host Identity) (Plan, error) {
	data, err := appleMetadata(ctx, client, intelMacCatalogURL, 16<<20)
	if err != nil {
		return Plan{}, err
	}
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return Plan{}, err
	}
	defer compressed.Close()
	data, err = io.ReadAll(io.LimitReader(compressed, 64<<20))
	if err != nil {
		return Plan{}, err
	}
	var catalog plist.Plist
	if err := xml.Unmarshal(data, &catalog); err != nil {
		return Plan{}, err
	}
	text := func(value plist.Value) string {
		if value.String == nil {
			return ""
		}
		return *value.String
	}
	versionRE := regexp.MustCompile(`<key>VERSION</key>\s*<string>([^<]+)</string>`)
	buildRE := regexp.MustCompile(`<key>BUILD</key>\s*<string>([^<]+)</string>`)
	var selected Plan
	for _, product := range catalog.Value.Dict["Products"].Dict {
		for _, pkg := range product.Dict["Packages"].Array {
			address := text(pkg.Dict["URL"])
			if !strings.HasSuffix(address, "/InstallAssistant.pkg") || trustedURL(address, "macos") != nil {
				continue
			}
			dist := text(product.Dict["Distributions"].Dict["English"])
			if dist == "" {
				continue
			}
			distribution, err := appleMetadata(ctx, client, dist, 2<<20)
			if err != nil {
				continue
			}
			version, build := versionRE.FindSubmatch(distribution), buildRE.FindSubmatch(distribution)
			if len(version) != 2 || len(build) != 2 || !intelMacVersionCompatible(host.Version, string(version[1])) ||
				(string(version[1]) == host.Version && string(build[1]) != host.Build) {
				continue
			}
			if pkg.Dict["Size"].Integer == nil {
				return Plan{}, errors.New("Apple installer size missing")
			}
			candidate := Plan{Host: host, Version: string(version[1]), Build: string(build[1]), Format: "pkgdmg", URL: address, Bytes: *pkg.Dict["Size"].Integer, IntegrityURL: text(pkg.Dict["IntegrityDataURL"])}
			// Prefer the exact host build. When Apple withdraws that installer,
			// use a current patch of the same macOS generation, shown in the UI.
			if selected.Version == "" || (candidate.Version == host.Version && selected.Version != host.Version) ||
				(candidate.Version == selected.Version && candidate.URL < selected.URL) ||
				(selected.Version != host.Version && semver.Compare("v"+candidate.Version, "v"+selected.Version) > 0) {
				selected = candidate
			}
		}
	}
	if selected.Version == "" {
		return Plan{}, errors.New("Apple 目录中没有与本机 macOS 主版本兼容的 Intel 完整安装包")
	}
	manifest, err := appleMetadata(ctx, client, selected.IntegrityURL, 8<<20)
	if err != nil {
		return Plan{}, err
	}
	hash := sha256.Sum256(manifest)
	selected.IntegritySHA256 = hex.EncodeToString(hash[:])
	return selected, selected.Validate()
}

// Apple's package catalog Digest can describe a different wrapping of a
// pkgdmg. Pin the HTTPS integrity manifest's SHA-256, verify every package
// chunk, and independently require Apple's package-signing chain instead.
func verifyIntelPackage(ctx context.Context, path string, plan Plan, transport http.RoundTripper) error {
	cache := filepath.Join(filepath.Dir(path), ".cage-integrity-"+plan.IntegritySHA256)
	var data []byte
	info, err := os.Lstat(cache)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return errors.New("invalid cached Apple integrity manifest")
		}
		data, err = os.ReadFile(cache)
	} else if errors.Is(err, os.ErrNotExist) {
		client := &http.Client{Transport: transport}
		data, err = appleMetadata(ctx, client, plan.IntegrityURL, 8<<20)
	} else {
		return err
	}
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != plan.IntegritySHA256 {
		return errors.New("Apple 安装完整性清单已变化")
	}
	if err := verifyIntelChunks(ctx, path, data); err != nil {
		return err
	}
	if err := verifyApplePackageSignature(ctx, path); err != nil {
		return err
	}
	if file, err := os.OpenFile(cache, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err == nil {
		_, writeErr := file.Write(data)
		return errors.Join(writeErr, file.Close())
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func verifyIntelChunks(ctx context.Context, path string, data []byte) error {
	if len(data) < 68 || string(data[:4]) != "CNKL" || binary.LittleEndian.Uint32(data[4:8]) != 36 || data[8] != 1 || data[9] != 1 || (data[10] != 1 && data[10] != 2) {
		return errors.New("invalid Apple package chunk manifest")
	}
	count := binary.LittleEndian.Uint64(data[12:20])
	offset := binary.LittleEndian.Uint64(data[20:28])
	signature := binary.LittleEndian.Uint64(data[28:36])
	if count == 0 || count > 1<<18 || offset != 36 || signature != 36+36*count || signature > uint64(len(data)) {
		return errors.New("invalid Apple package chunk table")
	}
	if data[10] == 2 {
		hash := sha256.Sum256(data[:signature])
		if len(data) != int(signature)+32 || !bytes.Equal(data[signature:], hash[:]) {
			return errors.New("invalid Apple chunk manifest checksum")
		}
	} else if len(data) != int(signature)+256 {
		return errors.New("invalid Apple chunk signature size")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	for index := uint64(0); index < count; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := data[36+index*36 : 36+(index+1)*36]
		size := binary.LittleEndian.Uint32(entry[:4])
		if size == 0 || size > 64<<20 {
			return errors.New("invalid Apple package chunk size")
		}
		hash := sha256.New()
		if _, err := io.CopyN(hash, file, int64(size)); err != nil {
			return err
		}
		if !bytes.Equal(hash.Sum(nil), entry[4:]) {
			return errors.New("Apple 安装包分块校验失败")
		}
	}
	var trailing [1]byte
	if n, err := file.Read(trailing[:]); n != 0 || err != io.EOF {
		return errors.New("Apple 安装包大小与完整性清单不符")
	}
	return nil
}
