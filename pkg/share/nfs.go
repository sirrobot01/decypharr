package share

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/facetfs/nfs4"
)

// NFSServer serves the library catalog over NFSv4.0.
type NFSServer struct {
	manager *manager.Manager
	export  *Export
	config  config.NFS
}

func NewNFS(mgr *manager.Manager, export *Export, cfg config.NFS) *NFSServer {
	return &NFSServer{manager: mgr, export: export, config: cfg}
}

func (s *NFSServer) Start(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-s.manager.IsReady():
	}

	networks, err := parseNetworks(s.config.AllowedNetworks)
	if err != nil {
		return fmt.Errorf("invalid NFS allowed networks: %w", err)
	}

	// A fixed handle key keeps client filehandles valid across restarts; the
	// resolver covers paths too long to embed in a handle.
	key, err := loadHandleKey(filepath.Join(config.GetMainPath(), "nfs", "handle.key"))
	if err != nil {
		return fmt.Errorf("load NFS handle key: %w", err)
	}

	log := logger.New("nfs")
	server := &nfs4.Server{
		// The export is shared with SMB and fronted by the on-disk cache, so
		// a client seek or a scanner re-reading a header costs a disk hit
		// rather than a fresh debrid session.
		FileSystem:        s.export.FileSystem(),
		HandleKey:         key,
		ResolveLongHandle: newResolver(s.manager).resolve,
		// ReadDelegations stays off: repair sweeps rewrite file content
		// outside this server, and a delegation is only safe when the NFS
		// server is the sole writer.
		Logger: func(err error) { log.Debug().Err(err).Msg("NFS connection fault") },
	}

	address := net.JoinHostPort(s.config.BindAddress, strconv.Itoa(int(s.config.Port)))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for NFS on %s: %w", address, err)
	}

	log.Info().Str("address", address).Msg("NFSv4 server started")

	err = server.Serve(ctx, &filteredListener{Listener: listener, networks: networks})
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("serve NFS: %w", err)
}

// loadHandleKey returns the persisted 32-byte filehandle key, creating it on
// first use.
func loadHandleKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// filteredListener drops connections from outside the allowed networks
// before they reach the protocol layer.
type filteredListener struct {
	net.Listener
	networks []netip.Prefix
}

func (l *filteredListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !allows(l.networks, conn.RemoteAddr()) {
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

func parseNetworks(values []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			addr, addrErr := netip.ParseAddr(value)
			if addrErr != nil {
				return nil, err
			}
			addr = addr.Unmap()
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		addr := prefix.Addr()
		bits := prefix.Bits()
		if addr.Is4In6() {
			addr = addr.Unmap()
			bits -= 96
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, bits))
	}
	if len(prefixes) == 0 {
		return nil, errors.New("at least one NFS allowed network is required")
	}
	return prefixes, nil
}

func allows(prefixes []netip.Prefix, remote net.Addr) bool {
	var addr netip.Addr
	switch value := remote.(type) {
	case *net.TCPAddr:
		if value.IP != nil {
			addr, _ = netip.ParseAddr(value.IP.String())
		}
	case *net.UDPAddr:
		if value.IP != nil {
			addr, _ = netip.ParseAddr(value.IP.String())
		}
	}
	if !addr.IsValid() {
		host, _, err := net.SplitHostPort(remote.String())
		if err != nil {
			return false
		}
		addr, err = netip.ParseAddr(host)
		if err != nil {
			return false
		}
	}
	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
