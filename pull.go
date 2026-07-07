package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// We pull from Docker Hub over the Registry v2 HTTP API. auth.docker.io hands out
// a short-lived bearer token, registry-1.docker.io serves manifests and blobs.
const (
	dockerAuth     = "https://auth.docker.io/token"
	dockerRegistry = "https://registry-1.docker.io"

	// The media types we're willing to accept for a manifest. Without these Hub can
	// fall back to an old schema-1 manifest.
	manifestAccept = "application/vnd.docker.distribution.manifest.v2+json," +
		"application/vnd.docker.distribution.manifest.list.v2+json," +
		"application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.oci.image.index.v1+json"
)

// manifest covers both an image manifest (Config + Layers) and a multi-arch
// index (Manifests). We tell them apart by which fields are populated.
type manifest struct {
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

// pull downloads an image and extracts it into images/<name>.
func pull(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: minidoc pull <image[:tag]>")
		os.Exit(1)
	}
	repo, tag := parseRef(args[0])
	if err := pullImage(repo, tag); err != nil {
		fmt.Fprintf(os.Stderr, "pull: %v\n", err)
		os.Exit(1)
	}
}

// parseRef splits "alpine", "alpine:3.19" or "user/repo:tag" into a full repo
// name and a tag. Official images get the implicit "library/" prefix.
func parseRef(ref string) (repo, tag string) {
	repo, tag = ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, tag
}

// localName is the images/ directory name for a repo (drop library/, / -> _).
func localName(repo string) string {
	return strings.ReplaceAll(strings.TrimPrefix(repo, "library/"), "/", "_")
}

func pullImage(repo, tag string) error {
	token, err := getToken(repo)
	if err != nil {
		return err
	}

	// Fetch the manifest. For a multi-arch image this is an index, so we pick the
	// linux/amd64 entry and fetch that manifest by digest.
	m, err := fetchManifest(repo, tag, token)
	if err != nil {
		return err
	}
	if len(m.Manifests) > 0 {
		digest := ""
		for _, sub := range m.Manifests {
			if sub.Platform.OS == "linux" && sub.Platform.Architecture == "amd64" {
				digest = sub.Digest
				break
			}
		}
		if digest == "" {
			return fmt.Errorf("no linux/amd64 image in %s:%s", repo, tag)
		}
		if m, err = fetchManifest(repo, digest, token); err != nil {
			return err
		}
	}
	if len(m.Layers) == 0 {
		return fmt.Errorf("manifest for %s:%s has no layers", repo, tag)
	}

	dest := filepath.Join(imagesDir, localName(repo))
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("clear %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	fmt.Printf("pulling %s:%s (%d layers)\n", repo, tag, len(m.Layers))
	for i, layer := range m.Layers {
		fmt.Printf("  layer %d/%d %s (%s)\n", i+1, len(m.Layers), short(layer.Digest), humanSize(layer.Size))
		if err := pullLayer(repo, layer.Digest, token, dest); err != nil {
			return err
		}
	}
	fmt.Printf("done: images/%s\n", localName(repo))
	return nil
}

// getToken asks the auth service for a pull-scoped bearer token.
func getToken(repo string) (string, error) {
	u := fmt.Sprintf("%s?service=registry.docker.io&scope=repository:%s:pull", dockerAuth, repo)
	resp, err := http.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth token: %s", resp.Status)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	return t.Token, nil
}

// fetchManifest gets the manifest for a tag or digest and parses it.
func fetchManifest(repo, ref, token string) (manifest, error) {
	var m manifest
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", dockerRegistry, repo, ref)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", manifestAccept)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("manifest %s: %s", ref, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	return m, nil
}

// pullLayer downloads one gzipped layer blob, verifies its sha256 digest, and
// extracts it over dest. The tar is streamed straight through gunzip so nothing
// large is buffered.
func pullLayer(repo, digest, token, dest string) error {
	u := fmt.Sprintf("%s/v2/%s/blobs/%s", dockerRegistry, repo, digest)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// Blobs redirect to a CDN with a presigned URL; Go's client drops the
	// Authorization header on the cross-host redirect, which is what we want.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blob %s: %s", short(digest), resp.Status)
	}

	// Hash the compressed bytes as they stream by, so we can check the digest.
	h := sha256.New()
	tee := io.TeeReader(resp.Body, h)
	gz, err := gzip.NewReader(tee)
	if err != nil {
		return err
	}
	defer gz.Close()

	if err := extractTar(gz, dest); err != nil {
		return err
	}
	// Drain the rest so the hash covers the whole blob (tar stops at its end
	// marker, before the gzip trailer).
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != digest {
		return fmt.Errorf("digest mismatch: got %s want %s", short(got), short(digest))
	}
	return nil
}

// extractTar unpacks a layer tar over dest, applying overlay whiteouts so deleted
// files from lower layers actually disappear in the flattened rootfs.
func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		base := filepath.Base(hdr.Name)
		parent := filepath.Dir(hdr.Name)

		// ".wh..wh..opq" hides everything from lower layers in this directory.
		if base == ".wh..wh..opq" {
			dir, err := secureJoin(dest, parent)
			if err != nil {
				return err
			}
			os.RemoveAll(dir)
			os.MkdirAll(dir, 0755)
			continue
		}
		// ".wh.<name>" deletes <name> from lower layers.
		if strings.HasPrefix(base, ".wh.") {
			target, err := secureJoin(dest, filepath.Join(parent, strings.TrimPrefix(base, ".wh.")))
			if err != nil {
				return err
			}
			os.RemoveAll(target)
			continue
		}

		target, err := secureJoin(dest, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			// Always writable so we can extract files into it; the exact mode
			// doesn't matter for a rootfs.
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			// Restore mode (chmod isn't subject to umask), so exec bits survive.
			os.Chmod(target, os.FileMode(hdr.Mode)&0777)
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Remove(target)
			os.Symlink(hdr.Linkname, target)
		case tar.TypeLink:
			os.MkdirAll(filepath.Dir(target), 0755)
			if src, err := secureJoin(dest, hdr.Linkname); err == nil {
				os.Remove(target)
				os.Link(src, target)
			}
		default:
			// Char/block/fifo need root to mknod; skip them. A rootfs rarely
			// needs them, and run builds its own /dev anyway.
		}
	}
	return nil
}

// secureJoin joins name onto base and refuses paths that escape base (a tar with
// "../" entries could otherwise write outside the image dir).
func secureJoin(base, name string) (string, error) {
	p := filepath.Join(base, name)
	if p != base && !strings.HasPrefix(p, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in layer: %q", name)
	}
	return p, nil
}

// short trims a sha256 digest to something readable in progress output.
func short(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
