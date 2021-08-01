package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/go-redis/redis/v8"
	"github.com/julienschmidt/httprouter"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/uber/jaeger-client-go"
	"github.com/uber/jaeger-client-go/config"
)

const serviceName = "playlist-api"

var environment = os.Getenv("ENVIRONMENT")
var redis_host = os.Getenv("REDIS_HOST")
var redis_port = os.Getenv("REDIS_PORT")
var jaeger_host_port = os.Getenv("JAEGER_HOST_PORT")

var rdb *redis.Client

func main() {
	cfg := config.Configuration{
		ServiceName: serviceName,
		Sampler: &config.SamplerConfig{
			Type:  "const",
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			LogSpans:           true,
			LocalAgentHostPort: jaeger_host_port,
		},
	}

	tracer, closer, err := cfg.NewTracer(config.Logger(jaeger.StdLogger))
	if err != nil {
		panic(fmt.Sprintf("Error: cannot init Jaeger: %v\n", err))
	}
	defer closer.Close()
	opentracing.SetGlobalTracer(tracer)

	router := httprouter.New()

	router.GET("/", func(rw http.ResponseWriter, r *http.Request, p httprouter.Params) {
		spanCtx, _ := tracer.Extract(
			opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(r.Header),
		)

		span := tracer.StartSpan("playlist-api: GET /", ext.RPCServerOption(spanCtx))
		defer span.Finish()

		cors(rw)

		ctx := opentracing.ContextWithSpan(context.Background(), span)
		playlistJson := getPlaylist(ctx)

		playlists := []playlist{}
		err := json.Unmarshal([]byte(playlistJson), &playlists)
		if err != nil {
			panic(err)
		}

		for pi := range playlists {
			vs := []video{}

			for vi := range playlists[pi].Videos {
				span, _ := opentracing.StartSpanFromContext(ctx, "playlist-api: videos-api GET /id")

				v := video{}

				req, err := http.NewRequest("GET", "http://videos-api:10010/"+playlists[pi].Videos[vi].Id, nil)
				if err != nil {
					panic(err)
				}
				span.Tracer().Inject(
					span.Context(),
					opentracing.HTTPHeaders,
					opentracing.HTTPHeadersCarrier(req.Header),
				)

				videoResp, err := http.DefaultClient.Do(req)
				span.Finish()

				if err != nil {
					fmt.Println(err)
					span.SetTag("error", true)
					break
				}

				defer videoResp.Body.Close()
				video, err := ioutil.ReadAll(videoResp.Body)
				if err != nil {
					panic(err)

				}

				err = json.Unmarshal(video, &v)
				if err != nil {
					panic(err)
				}

				vs = append(vs, v)

			}
			playlists[pi].Videos = vs

		}

		playlistBytes, err := json.Marshal(playlists)
		if err != nil {
			panic(err)
		}

		reader := bytes.NewReader(playlistBytes)
		if b, err := ioutil.ReadAll(reader); err == nil {
			fmt.Fprint(rw, string(b))
		}

	})

	r := redis.NewClient(&redis.Options{
		Addr: redis_host + ":" + redis_port,
		DB:   0,
	})

	rdb = r

	fmt.Println("Running...")
	log.Fatal(http.ListenAndServe(":10010", router))
}

type playlist struct {
	Id     string  `json:"id"`
	Name   string  `json:"name"`
	Videos []video `json:"videos"`
}

type video struct {
	Id          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ImageURL    string `json:"imageurl"`
	Url         string `json:"url"`
}

func getPlaylist(ctx context.Context) (response string) {
	println("getPlaylist fetching data")

	span, _ := opentracing.StartSpanFromContext(ctx, "playlist-api: redis-get")
	defer span.Finish()

	playlistData, err := rdb.Get(ctx, "playlist").Result()
	if err != nil {
		fmt.Println(err)
		fmt.Println(" error occurred retrieving playlist from Redis server")
		span.SetTag("error", true)
		return "[]"
	}

	return playlistData
}

func cors(writer http.ResponseWriter) {
	if environment == "DEBUG" {
		writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-MY-API-Version")
		writer.Header().Set("Access-Control-Allow-Credentials", "true")
		writer.Header().Set("Access-Control-Allow-Origin", "*")
	}
}
