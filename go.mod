module github.com/perlin-network/wavelet

go 1.12

replace github.com/go-interpreter/wagon => github.com/perlin-network/wagon v0.3.1-0.20180825141017-f8cb99b55a39

require (
	github.com/buaazp/fasthttprouter v0.1.1
	github.com/chzyer/logex v1.1.10 // indirect
	github.com/chzyer/readline v0.0.0-20180603132655-2972be24d48e
	github.com/chzyer/test v0.0.0-20180213035817-a1ea475d72b1 // indirect
	github.com/davecgh/go-spew v1.1.1
	github.com/diamondburned/tcell v1.1.7
	github.com/diamondburned/tview/v2 v2.1.0
	github.com/fasthttp/websocket v1.4.0
	github.com/gogo/protobuf v1.2.1
	github.com/golang/protobuf v1.3.1 // indirect
	github.com/golang/snappy v0.0.1
	github.com/google/btree v1.0.0
	github.com/huandu/skiplist v0.0.0-20180112095830-8e883b265e1b
	github.com/kr/pretty v0.1.0 // indirect
	github.com/perlin-network/life v0.0.0-20190521143330-57f3819c2df0
	github.com/perlin-network/noise v0.0.0-20190527211417-79abfb78fdba
	github.com/phf/go-queue v0.0.0-20170504031614-9abe38d0371d
	github.com/pkg/errors v0.8.1
	github.com/rcrowley/go-metrics v0.0.0-20181016184325-3113b8401b8a
	github.com/rs/zerolog v1.14.3
	github.com/spf13/pflag v1.0.3
	github.com/stretchr/testify v1.3.0
	github.com/syndtr/goleveldb v1.0.0
	github.com/valyala/fasthttp v1.3.0
	github.com/valyala/fastjson v1.4.1
	golang.org/x/crypto v0.0.0-20190611184440-5c40567a22f8
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4
	google.golang.org/grpc v1.21.0
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
	gopkg.in/urfave/cli.v1 v1.20.0
	gopkg.in/yaml.v2 v2.2.2 // indirect
)

replace github.com/diamondburned/tview/v2 => ../../tview
