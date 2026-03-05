# Update Cloudflare DNS Docker Container

This docker container periodically updates a Cloudflare DNS A record with an external
IP address. This is useful if you have a dynamic IP address and want to update
the current IP in a Cloudflare DNS record.

## Usage

### Docker

```shell
docker run -d \
    --name update-cloudflare \
    -e CF_API_TOKEN=<your cloudflare api token> \
    -e ZONE_ID=<your cloudflare zone id> \
    -e DNS_NAME=myhost.domain.com \
    ghcr.io/jpflouret/update-cloudflare:latest
```

### Kubernetes Helm Chart

Add the chart repo:
```shell
helm repo add update-cloudflare https://jpflouret.github.io/update-cloudflare/
helm repo update
```

Create `my-values.yaml` configuration file with default values:
```shell
helm show values update-cloudflare/update-cloudflare > my-values.yaml
```

Edit the `my-values.yaml` file and set the values as required:
| Key           | Required? | Description                                                                       | Default                                                    |
| ------------- | --------- | --------------------------------------------------------------------------------- | -----------------------------------------------------------|
| `dnsName`     | Yes       | Host name to update                                                               | `""`                                                       |
| `zoneId`      | Yes       | Cloudflare zone ID to update                                                      | `""`                                                       |
| `dnsTTL`      | No        | TTL for the DNS record (1 = Cloudflare Auto TTL)                                  | `1`<br>(Default in executable)                             |
| `checkIPURL`  | No        | URL to check the public IP address                                                | `http://checkip.amazonaws.com/`<br>(Default in executable) |
| `sleepPeriod` | No        | Sleep period between IP address checks                                            | `5m`                                                       |
| `tolerations` | No        | List of kubernetes node taints that are tolerated by the `update-cloudflare` pods | Empty                                                      |
| `nodeSelector`| No        | List of labels used to select which nodes can run `update-cloudflare` pods        | Empty                                                      |

Recommended tolerations to allow execution in control plane nodes:
```yaml
tolerations:
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
  - key: node-role.kubernetes.io/master
    operator: Exists
    effect: NoSchedule
```

#### Cloudflare Credentials
The Cloudflare API token is supplied to the pod with a kubernetes secret.
The secret should contain the following key:
- `CF_API_TOKEN`

You can create the secret using `kubectl`:
```shell
kubectl create secret generic cloudflare-credentials \
  --namespace <namespace> \
  --from-literal=CF_API_TOKEN=<your cloudflare api token>
```
> [!TIP]
> Add a space in front of the above command to prevent bash from
> storing the command in the history file.

When installing the chart, set `secret.existingSecret` to the name of
the secret created above (`cloudflare-credentials` in this example):
```shell
helm install \
  my-update-cloudflare \
  update-cloudflare/update-cloudflare \
  --namespace <namespace> \
  --values my-values.yaml \
  --set=secret.existingSecret=cloudflare-credentials
```

Alternatively, the secret can be created at chart installation time using
the following configuration values:
| Key                  | Required?                               | Description                                                  | Default |
| -------------------- | --------------------------------------- | ------------------------------------------------------------ | ------- |
| `secret.create`      | No                                      | Set to `true` to create a secret as part of the Helm release | `false` |
| `secret.cfApiToken`  | Yes if `secret.create` is set to `true` | Cloudflare API token to use when creating the secret         | `""`    |

#### Metrics
The pod exposes prometheus metrics on port `8080` on the `/metrics` path.
You can create a service and configure prometheus to scrape the metrics
endpoint automatically using service annotations.

Metrics configuration values:
| Key                   | Required? | Description                                 | Default     |
| --------------------- | --------- | ------------------------------------------- | ----------- |
| `service.create`      | No        | Create a service for the metrics endpoint.  | `false`     |
| `service.type`        | No        | Type of service metrics endpoint.           | `ClusterIP` |
| `service.annotations` | No        | Annotations to add to the metrics endpoint. | Empty       |

You can configure prometheus to scrape the service endpoint automatically by
adding the following annotations to the service (in `my-values.yaml`):
```yaml
service:
  create: true
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/path: /metrics
    prometheus.io/port: "8080"
```

#### Service Account
If you need to, you can create a kubernetes service account for use with
`update-cloudflare` pods using the following configuration variables:

| Key                     | Required? | Description                                          | Default                                 |
| ----------------------- | --------- | ---------------------------------------------------- | --------------------------------------- |
| `serviceAccount.create` | No        | Create a kubernetes service account for the release. | `false`                                 |
| `serviceAccount.name`   | No        | Name of the service account to use                   | Defaults to the full helm release name. |
