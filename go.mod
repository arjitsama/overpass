module github.com/arjitsama/overpass

go 1.26.5

require (
	github.com/agentnameservice/agent-trust-discovery v0.0.0
	github.com/agentnameservice/ans-sdk-go v0.1.18
	github.com/go-jose/go-jose/v4 v4.1.5
	github.com/gowebpki/jcs v1.0.1
	github.com/joshuaferrara/go-satellite v0.0.0-20220611180459-512638c64e5b
	go.yaml.in/yaml/v3 v3.0.4
	modernc.org/sqlite v1.59.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.3 // indirect
	github.com/go-chi/chi/v5 v5.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/miekg/dns v1.1.73 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/agentnameservice/agent-trust-discovery => ./third_party/agent-trust-discovery
