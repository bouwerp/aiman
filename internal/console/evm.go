package console

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// EVM tab (P9): the console lists a run's recent EVM transactions and shows
// one's call tree with decoded revert reasons. Both go to serve as the
// operator; serve reads the node through the edge's read-only relay.

// EVM API paths.
const (
	EVMTxsPath   = "/api/evm/txs"
	EVMTracePath = "/api/evm/trace"
)

var (
	evmServiceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	evmTxRe      = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

func init() {
	apiRoutes[EVMTxsPath] = apiRoute{http.MethodGet, (*Server).handleEVMTxs}
	apiRoutes[EVMTracePath] = apiRoute{http.MethodGet, (*Server).handleEVMTrace}
	staticFiles["/evm.js"] = [2]string{"web/evm.js", "text/javascript; charset=utf-8"}
	staticFiles["/evm.css"] = [2]string{"web/evm.css", "text/css; charset=utf-8"}
}

// evmParams reads run and service, checks the run is the one shown, and
// returns serve's params; false once it has answered.
func (s *Server) evmParams(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	q := r.URL.Query()
	params := map[string]any{}
	if svc := strings.TrimSpace(q.Get("service")); svc != "" {
		if !evmServiceRe.MatchString(svc) {
			badParams(w, "service must be a service name")
			return nil, false
		}
		params["service"] = svc
	}
	if !s.sameRun(w, q.Get("run")) {
		return nil, false
	}
	params["run"] = s.currentRun()
	return params, true
}

func (s *Server) handleEVMTxs(w http.ResponseWriter, r *http.Request) {
	params, ok := s.evmParams(w, r)
	if !ok {
		return
	}
	if b := r.URL.Query().Get("blocks"); b != "" {
		n, err := strconv.Atoi(b)
		if err != nil || n <= 0 || n > 64 {
			badParams(w, "blocks must be 1-64")
			return
		}
		params["blocks"] = n
	}
	s.relay(w, r, "harness.evm_txs", params)
}

func (s *Server) handleEVMTrace(w http.ResponseWriter, r *http.Request) {
	tx := strings.TrimSpace(r.URL.Query().Get("tx"))
	if !evmTxRe.MatchString(tx) {
		badParams(w, "tx must be a transaction hash: 0x and 64 hex digits")
		return
	}
	params, ok := s.evmParams(w, r)
	if !ok {
		return
	}
	params["tx"] = tx
	s.relay(w, r, "harness.evm_trace", params)
}
