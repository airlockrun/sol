package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const AttachmentMaterializationCapability = "attachments.v1"

// AttachmentMaterialization is a host-only exact-object download grant. It is
// transported separately from model tool requests and never contains credentials.
type AttachmentMaterialization struct {
	Scope    string `json:"scope"`
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	URL      string `json:"url"`
	Origin   string `json:"origin"`
}

type MaterializedAttachment struct {
	Path   string `json:"path"`
	Reused bool   `json:"reused"`
}

var attachmentIdentity = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (t *WSTransport) RequireAttachmentMaterialization(ctx context.Context) error {
	response, err := t.roundTrip(ctx, message{Type: "tools", ID: t.allocID()})
	if err != nil {
		return err
	}
	for _, capability := range response.Capabilities {
		if capability == AttachmentMaterializationCapability {
			return nil
		}
	}
	return errors.New("toolserver requires rebuild: missing attachments.v1; rebuild the agent-builder image with matching Sol source and recreate workspace toolservers")
}

func (t *WSTransport) MaterializeAttachment(ctx context.Context, request AttachmentMaterialization) (MaterializedAttachment, error) {
	response, err := t.roundTrip(ctx, message{Type: "materialize_attachment", ID: t.allocID(), Attachment: &request})
	if err != nil {
		return MaterializedAttachment{}, err
	}
	if response.Error != "" {
		return MaterializedAttachment{}, errors.New(response.Error)
	}
	if response.Materialized == nil {
		return MaterializedAttachment{}, errors.New("toolserver requires attachments.v1; rebuild the agent-builder image")
	}
	return *response.Materialized, nil
}

func (e *RemoteExecutor) MaterializeAttachment(ctx context.Context, request AttachmentMaterialization) (MaterializedAttachment, error) {
	transport, ok := e.transport.(interface {
		MaterializeAttachment(context.Context, AttachmentMaterialization) (MaterializedAttachment, error)
	})
	if !ok {
		return MaterializedAttachment{}, errors.New("executor transport requires attachments.v1")
	}
	return transport.MaterializeAttachment(ctx, request)
}

func materializeAttachment(ctx context.Context, rootPath string, request AttachmentMaterialization) (MaterializedAttachment, error) {
	if err := ctx.Err(); err != nil {
		return MaterializedAttachment{}, err
	}
	if !attachmentIdentity.MatchString(request.ID) || !attachmentIdentity.MatchString(request.Scope) || request.Filename == "" || len(request.Filename) > 255 || request.Filename == "." || request.Filename == ".." || strings.ContainsAny(request.Filename, "/\\\x00\r\n") || request.Size < 0 || request.Size > 1<<30 {
		return MaterializedAttachment{}, errors.New("invalid attachment identity, filename or size")
	}
	digest, err := hex.DecodeString(request.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != request.SHA256 {
		return MaterializedAttachment{}, errors.New("invalid attachment SHA-256")
	}
	source, err := url.Parse(request.URL)
	if err != nil || source.User != nil || source.Fragment != "" || source.Host == "" || (source.Scheme != "http" && source.Scheme != "https") || request.Origin != source.Scheme+"://"+source.Host {
		return MaterializedAttachment{}, errors.New("invalid exact-object storage origin")
	}
	if err := os.MkdirAll(rootPath, 0700); err != nil {
		return MaterializedAttachment{}, err
	}
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return MaterializedAttachment{}, errors.New("attachment root must be a real directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return MaterializedAttachment{}, err
	}
	defer root.Close()
	if err := root.Mkdir(request.ID, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return MaterializedAttachment{}, err
	}
	info, err = root.Lstat(request.ID)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return MaterializedAttachment{}, errors.New("attachment directory must not be a symlink")
	}
	directory, err := root.OpenRoot(request.ID)
	if err != nil {
		return MaterializedAttachment{}, err
	}
	defer directory.Close()
	path := filepath.Join(rootPath, request.ID, request.Filename)
	if info, err := directory.Lstat(request.Filename); err == nil && info.Mode().IsRegular() && info.Size() == request.Size {
		file, err := directory.Open(request.Filename)
		if err == nil {
			opened, statErr := file.Stat()
			if statErr == nil && opened.Mode().IsRegular() && opened.Size() == request.Size {
				hash := sha256.New()
				size, readErr := io.Copy(hash, io.LimitReader(attachmentReader{ctx: ctx, body: file}, request.Size+1))
				closeErr := file.Close()
				if readErr == nil && closeErr == nil && size == request.Size && hex.EncodeToString(hash.Sum(nil)) == request.SHA256 {
					if err := ctx.Err(); err != nil {
						return MaterializedAttachment{}, err
					}
					return MaterializedAttachment{Path: path, Reused: true}, nil
				}
			} else {
				_ = file.Close()
			}
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("attachment redirects are forbidden") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL, nil)
	if err != nil {
		return MaterializedAttachment{}, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			return MaterializedAttachment{}, fmt.Errorf("attachment GET failed: %w", requestError.Err)
		}
		return MaterializedAttachment{}, errors.New("attachment GET failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return MaterializedAttachment{}, fmt.Errorf("attachment GET returned %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != request.Size {
		return MaterializedAttachment{}, errors.New("attachment download length mismatch")
	}
	// A unique file inside the directory is atomically renamed only after both
	// actual length and digest match. Root-relative operations cannot escape via
	// symlinks, including concurrent changes made by workspace commands.
	tmpName := ".download-" + rand.Text()
	file, err := directory.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return MaterializedAttachment{}, err
	}
	defer directory.Remove(tmpName)
	hash := sha256.New()
	size, readErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, request.Size+1))
	closeErr := file.Close()
	if readErr != nil {
		return MaterializedAttachment{}, readErr
	}
	if closeErr != nil {
		return MaterializedAttachment{}, closeErr
	}
	if size != request.Size || hex.EncodeToString(hash.Sum(nil)) != request.SHA256 {
		return MaterializedAttachment{}, errors.New("attachment download integrity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return MaterializedAttachment{}, err
	}
	if err := directory.Rename(tmpName, request.Filename); err != nil {
		return MaterializedAttachment{}, err
	}
	return MaterializedAttachment{Path: path}, nil
}

type attachmentReader struct {
	ctx  context.Context
	body io.Reader
}

func (r attachmentReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.body.Read(p)
}
