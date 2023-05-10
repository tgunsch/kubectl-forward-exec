# forward-exec

Simple plugin that forward a local ports to a service or pod, run a command on local machine and finally stop the port forward.
This is useful for doing requests (e.g. via `curl`) on a pod, which for security reasons don't have a shell included.

## Examples

Show help:
```
kubectl forward-exec -h
```


Do port forward on port 8080 to pod `httpod`, execute the `curl` locally and stop the port-forwards afterward. 
```shell
kubectl forward-exec pod/httpod 8080 -- curl http://localhost:8080/api/status/200
```
Note: For the tests above, a [httpod](https://github.com/tgunsch/httpod) pod was started:

```shell
kubectl run httpod --image=ghcr.io/tgunsch/httpod:latest-slim --port=8080
```


Do a port forward from local port 8080 to port 80 of service `my-service` in namespace `my-namespace` and context `prod`:
```shell
kubectl forward-exec --context prod -n my-namespace svc/my-service 8080:80 -- curl http://localhost:8080/whatever
```

