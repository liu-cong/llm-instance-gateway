# Endpoint Picker Protocol

The Endpoint Picker, or EPP, is a core component of the inference extension. Ultimately it's
responsible for picking an endpoint from the `InferencePool`. A reference implementation can be
found [here](../../../pkg/ext-proc/).

## Proxy Protocol

This is the protocol between the EPP and the proxy (e.g, Envoy).

The EPP MUST implement the Envoy
[external processing service](https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/ext_proc/v3/external_processor)protocol.

For each HTTP request, the EPP MUST communicate to the proxy the picked model server endpoint, via
adding the `target-pod` HTTP header in the request, or otherwise return an error.

## Model Server Protocol

This is the protocol between the EPP and the model servers.

### Inference API Protocol

The model server MUST implement OpenAI’s [Completions](https://platform.openai.com/docs/api-reference/completions)
and [Chat](https://platform.openai.com/docs/api-reference/chat) APIs.

### Metrics Reporting

The inference extension scrapes metrics from the model servers to make optimal request scheduling
decisions. The model servers MUST provide the following metrics via a Prometheus endpoint. The exact
metric names don't necessarily need to be the same as the recommended names here, however the
metric types and semantics MUST follow this doc.

Note the requirements here are aligned with the
[model server metrics standardization](https://docs.google.com/document/d/1SpSp1E6moa4HSrJnS4x3NpLuj88sMXr2tbofKlzTZpk)
effort.

The corresponding metrics in vLLM are also shown in the table below, as vLLM is already integrated
into the reference endpoint picker implementation.

| Metric | Type | Description | vLLM metric |
| ----- | ---- | ---- | ---- |
| TotalQueuedRequests         | Gauge     | The current total number of requests in the queue.| `vllm:num_requests_waiting`|
| KVCacheUtilization| Gauge     | The current KV cache utilization in percentage.| `vllm:gpu_cache_usage_perc`|


### LoRA Adapter Serving

Model servers that support dynamic LoRA serving can benefit from the LoRA affinity algorithm. Note
the current algorithm in the reference EPP is highly biased towards vLLM's current dynamic LoRA 
implementation.

The model servers MUST support serving a LoRA adapter specified in the `model` argument of the
request, provided the requested adapter is valid.

The model server MUST expose the following LoRA adapter information via a RESTful API with response
in JSON :

* `Config` 
  * `LoRAEnabled`: boolean, whether dynamic LoRA serving is enabled.
  *  `MaxActiveAdapter`: integer, the maximum number of adapters that can be loaded to GPU memory to
  serve a batch. Requests will be queued if the model server has reached MaxActiveAdapter and cannot
  load the requested adapter. 
* `State`
  * `ActiveAdapters`: List[string], a list of adapters that are currently loaded in GPU memory and
  ready to serve requests.

This is an example API endpoint and response:
```
GET ${server_endpoint}/adapters/info
```

```
{
    "config": {
        "enabled": true,
        "maxActiveAdapters": 4,
    },
    "state": {
        "activeAdapters": ["adapter1", "adapter2"]
    }
}
```

NOTE: Currently in vLLM v0.6.6, LoRA info is exposed in the `vllm:lora_requests_info` metric, where
`MaxActiveAdapters` is exposed as a string label `max_lora`, and `ActiveAdapters` as a comma
separated string label `running_lora_adapters`. We will use [this issue](https://github.com/vllm-project/vllm/issues/10086)
to track integration efforts with vLLM.