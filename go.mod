module github.com/Laisky/laisky-blog-graphql

go 1.12

require (
	github.com/99designs/gqlgen v0.8.3
	github.com/Laisky/go-utils v1.4.1
	github.com/Laisky/zap v1.9.2
	github.com/gin-contrib/pprof v1.2.0
	github.com/gin-gonic/gin v1.4.0
	github.com/gomarkdown/markdown v0.0.0-20190222000725-ee6a7931a1e4
	github.com/pkg/errors v0.8.1
	github.com/spf13/pflag v1.0.3
	github.com/spf13/viper v1.1.1
	github.com/vektah/gqlparser v1.1.2
	gopkg.in/mgo.v2 v2.0.0-20180705113604-9856a29383ce
)

replace github.com/ugorji/go v1.1.4 => github.com/ugorji/go/codec v0.0.0-20190204201341-e444a5086c43
