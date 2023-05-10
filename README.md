# forward-exec

Simple plugin that is a wrapper around port-forward; i.e. Forward one or more local ports to a pod, run a command on local machine and finally stop the port forward.
This is useful for doing requests (e.g. via `curl`) on a pod, which for security reasons don't have a shell included.

## Examples

Show help:
```
kubectl forward-exec -h
```

> :warning: The port-forward takes a little time until established. Therefore, the connection might not be available when the command is run.
> Instead of implement a sleep into the plugin, the command must be prepared that the port-forward is not established.

Do port forward on port 8080 to pod `httpod`, execute the `curl` locally and stop the port-forwards afterward. The `retry` feature of `curl` is used to wait until connection is established:
```
kubectl forward-exec pod/httpod 8080 -- curl --connect-timeout 5 --retry 3 --retry-connrefused http://localhost:8080/api/status/200
```

Doing the same, but simply sleep 2 seconds before executing the `curl`:
```
kubectl forward-exec pod/httpod 8080 -- "sleep 2; curl http://localhost:8080/api/status/200"
```

Doing the same, but use netcat to wait until connection established:
```
kubectl forward-exec pod/httpod 8080 -- "while ! nc -z localhost 8080; do sleep 0.1; done; curl http://localhost:8080/api/status/200"
```

Do a port forward from local port 8080 to port 80 of service `my-service` in namespace `my-namespace` and context `prod`:
```
kubectl forward-exec --context prod -n my-namespace svc/my-service 8080:80 -- curl --connect-timeout 5 --retry 3 --retry-connrefused http://localhost:8080/whatever
```

Note: For the tests above, a [httpod](https://github.com/tgunsch/httpod) pod was started:

```shell
kubectl run httpod --image=ghcr.io/tgunsch/httpod:latest-slim --port=8080
```
