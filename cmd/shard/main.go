package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/modules/consensus"
	"gitlab.com/NebulousLabs/Sia/modules/gateway"

	"github.com/julienschmidt/httprouter"
)

type server struct {
	shard *SHARD
}

func (s *server) handlerSynced(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	io.WriteString(w, strconv.FormatBool(s.shard.Synced()))
}

func (s *server) handlerHeight(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
	io.WriteString(w, strconv.Itoa(int(s.shard.Height())))
}

func (s *server) handlerHost(w http.ResponseWriter, req *http.Request, ps httprouter.Params) {
	ann, ok := s.shard.HostAnnouncement(ps.ByName("pubkey"))
	if !ok {
		http.Error(w, "No record of that host", http.StatusNotFound)
		return
	}
	w.Write(ann)
}

func newServer(shard *SHARD) http.Handler {
	srv := &server{shard}
	mux := httprouter.New()
	mux.GET("/synced", srv.handlerSynced)
	mux.GET("/height", srv.handlerHeight)
	mux.GET("/host/:pubkey", srv.handlerHost)
	return mux
}

var (
	// to be supplied at build time
	githash   = "?"
	goversion = "?"
	builddate = "?"
)

func main() {
	persistDir := flag.String("d", ".", "directory where server state is stored")
	rpcAddr := flag.String("r", ":9381", "host:port that the gateway listens on")
	apiAddr := flag.String("a", ":8080", "host:port that the API server listens on")
	flag.Parse()

	if len(os.Args) == 2 && os.Args[1] == "version" {
		log.SetFlags(0)
		log.Printf("shard v0.1.0\nCommit:     %s\nRelease:    %s\nGo version: %s %s/%s\nBuild Date: %s\n",
			githash, build.Release, runtime.Version(), runtime.GOOS, runtime.GOARCH, builddate)
		return
	} else if len(os.Args) != 1 {
		flag.Usage()
		return
	}

	g, err := gateway.New(*rpcAddr, true, filepath.Join(*persistDir, "gateway"))
	if err != nil {
		log.Fatal(err)
	}
	cs, err := consensus.New(g, true, filepath.Join(*persistDir, "consensus"))
	if err != nil {
		log.Fatal(err)
	}
	shard, err := newSHARD(cs, newJSONPersist(*persistDir))
	if err != nil {
		log.Fatal(err)
	}

	srv := newServer(shard)
	log.Printf("Listening on %v...", *apiAddr)
	log.Fatal(http.ListenAndServe(*apiAddr, srv))
}