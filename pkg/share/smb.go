package share

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/facetfs/smb"
)

// SMBServer serves the library catalog over SMB2/SMB3. Experimental until
// facetfs's SMB client acceptance matrix (Windows, macOS, Linux) has passed.
type SMBServer struct {
	manager *manager.Manager
	export  *Export
	config  config.SMB
}

func NewSMB(mgr *manager.Manager, export *Export, cfg config.SMB) *SMBServer {
	return &SMBServer{manager: mgr, export: export, config: cfg}
}

func (s *SMBServer) Start(ctx context.Context) error {
	if s.config.Username == "" || s.config.Password == "" {
		return errors.New("SMB requires a username and password: the server grants no anonymous access")
	}

	select {
	case <-ctx.Done():
		return nil
	case <-s.manager.IsReady():
	}

	networks, err := parseNetworks(s.config.AllowedNetworks)
	if err != nil {
		return fmt.Errorf("invalid SMB allowed networks: %w", err)
	}

	// SMB reconnects re-open files by path, so unlike NFS there is no handle
	// key or resolver to persist. Reads go through the same cached export NFS
	// serves.
	log := logger.New("smb")
	server := &smb.Server{
		FileSystem: s.export.FileSystem(),
		Authenticator: &singleUser{
			user: s.config.Username,
			hash: smb.NTHash(s.config.Password),
		},
		ShareName:      s.config.ShareName,
		ServerName:     "DECYPHARR",
		RequireSigning: s.config.RequireSigning,
		Logger:         func(err error) { log.Debug().Err(err).Msg("SMB connection fault") },
	}

	address := net.JoinHostPort(s.config.BindAddress, strconv.Itoa(int(s.config.Port)))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for SMB on %s: %w", address, err)
	}

	log.Info().Str("address", address).Str("share", s.config.ShareName).Msg("SMB server started (experimental)")

	err = server.Serve(ctx, &filteredListener{Listener: listener, networks: networks})
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("serve SMB: %w", err)
}

// singleUser authenticates one account. The domain is ignored: Windows
// clients send their machine name, macOS sends whatever the user typed, and
// a single-user share gains nothing from pinning it.
type singleUser struct {
	user string
	hash []byte
}

func (a *singleUser) NTHash(_ context.Context, _, user string) ([]byte, error) {
	if !strings.EqualFold(user, a.user) {
		return nil, errors.New("unknown user")
	}
	return append([]byte(nil), a.hash...), nil
}
