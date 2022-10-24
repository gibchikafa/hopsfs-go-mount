module logicalclocks.com/hopsfs-mount

go 1.15

require (
	bazil.org/fuse v0.0.0-20200524192727-fb710f7dfd05
	github.com/BurntSushi/toml v0.4.1 // indirect
	github.com/antonfisher/nested-logrus-formatter v1.3.1
	github.com/colinmarc/hdfs/v2 v2.2.0
	github.com/golang/mock v1.6.0
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.7.0
	golang.org/x/net v0.0.0-20210405180319-a5a99cb37ef4
	golang.org/x/sys v0.0.0-20210510120138-977fb7262007
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
)

replace github.com/colinmarc/hdfs/v2 v2.2.0 => /home/gibson/work/hopsfs-go-client

//replace github.com/colinmarc/hdfs/v2 v2.2.0 => /home/salman/code/hops/hopsfs-go/hopsfs-go-client

//replace bazil.org/fuse v0.0.0-20200524192727-fb710f7dfd05 => /home/salman/code/hops/hopsfs-go/fuse
