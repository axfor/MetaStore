# Use goreman to run `go install github.com/mattn/goreman@latest`
./metaStore --id 1 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 8080
./metaStore --id 2 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 8081
./metaStore --id 3 --cluster http://127.0.0.1:12379,http://127.0.0.1:22379,http://127.0.0.1:32379 --port 8082


curl -L http://127.0.0.1:8080/my-key 
curl -L http://127.0.0.1:8081/my-key
curl -L http://127.0.0.1:8082/my-key


curl -L http://127.0.0.1:8080/my-key -XPUT -d 2222
curl -L http://127.0.0.1:8081/my-key -XPUT -d 333333
curl -L http://127.0.0.1:8082/my-key -XPUT -d 6666