# Model Server Protocol for Gateway API Inference Extension

## Inference API Protocol

The model server MUST implement OpenAI’s [Completions](https://platform.openai.com/docs/api-reference/completions)
and [Chat](https://platform.openai.com/docs/api-reference/chat) API. In the future we are open to
supporting more API protocols.

<details>
<summary>Why?</summary>
The extension makes intelligent request scheduling decisions based on certain information from the
request body, such as the `model` field.
</details>

## Metrics Reporting

The inference extension scrapes metrics from the model servers to make optimal request scheduling
decisions. The model servers SHOULD provide the following metrics via a Prometheus endpoint. While
the metric names may differ slightly in different model servers, the metric types MUST be the same.
We will align with the
[model server metrics standardization](https://docs.google.com/document/d/1SpSp1E6moa4HSrJnS4x3NpLuj88sMXr2tbofKlzTZpk)
effort.

We also show the metrics in vLLM, which is already integrated into the inference extension. We will
add more model server once they are integrated.

| Metric | Type | Description | vLLM metric |
| ----- | ---- | ---- | ---- |
| TotalQueuedRequests         | Gauge     | The current total number of requests in the queue.| `vllm:num_requests_waiting`|
| KVCacheUtilization| Gauge     | The current KV cache utilization in percentage.| `vllm:gpu_cache_usage_perc`|


### Future Metrics
The following metrics MAY be needed in the future for further optimization.

| Metric |Type | Description | vLLM   metric |
| ----- | ---- | ---- | ---- |
| TotalTokensInCurrentBatch   | Gauge     | Number of tokens in the current batch.| `vllm:num_tokens_running`|
| TotalQueuedTokens| Gauge     | The current total number of tokens in the queued requests.| `vllm:num_tokens_waiting` (need to be added)|
| MaxTokenCapacity| Gauge     | The total size of the KV cache in number of tokens.| `vllm:max_token_capacity` <br> NOTE: This info is available indirectly in [`cache_config_info`](https://github.com/vllm-project/vllm/blob/15702038642192002cd8973cf8948751b750fd07/vllm/engine/metrics.py#L551) metric already , and also  proposed in [model server metrics standardization](https://docs.google.com/document/d/1SpSp1E6moa4HSrJnS4x3NpLuj88sMXr2tbofKlzTZpk). |
| TimePerPrefillToken | Histogram | The prefill latency per token in the last W seconds. W will be decided by simulation/benchmarking.  In time series metric the latency is typically reported as Histogram and we can derive the average from the Histogram. | `vllm:time_to_first_token_seconds` | 
| TimePerDecodeToken | Histogram | The decode latency per token in the last W seconds. W will be decided by simulation/benchmarking. | `vllm:time_per_output_token_seconds` | 

## LoRA Adapter Serving

### Dynamic LoRA Serving

Model servers that support dynamic LoRA serving can gain additional benefit from the inference
extension's LoRA affinity algorithm. While dynamic LoRA serving is quite new and evolving, and there
is no common standard, the inference extension generally expects the following behavior.

* Support running multiple LoRA adapters in parallel in the same decode batch.
* Dynamically load/unload adapters in GPU memory from/to a cahe (e.g., in host memory) depending on
  the requested adapters in the current batch.

The model server SHOULD expose the following information via an API:

* AdapterConfig 
  * LoRAEnabled: Whether dynamic LoRA serving is enabled.
  *  MaxActiveAdapter: Maximum number of adapters that can be loaded to GPU memory to serve a batch.
  Requests will be queued if the model server has reached MaxActiveAdapter and cannot load the
  requested adapter. In vLLM, this is currently exposed as a string label `max_lora` in the
  `vllm:lora_requests_info` metric.
* AdapterState
  * ActiveAdapters: A list of adapters that are currently loaded in GPU memory and ready to servce
  requests. In vLLM, this is currently exposed as a comma separated string label `running_lora_adapters`
  in the `vllm:lora_requests_info` metric.

The API MAY look like this:
```
GET ${server_endpoint}/adapters/info
```

And the response MAY look like this:
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

#### Dynamically Register/Unregister Adapters

Model servers SHOULD have APIs to dynamically register/unregister models (usually LoRA adapters).
This enables platform teams to multiplex multiple LoRA adapters on shared model servers and
dynamically rollout LoRA adapters. 

NOTE this is not a strict requirement from the inference extension, but a critical feature for CI/CD
integration.

While we don’t intend to dictate how model servers should implement this API, a reference REST API
MAY look this:

```
POST ${server_endpoint}/adapters/{adapter-id}
{
        "path": "path/to/my/adapter"
}

DELETE ${server_endpoint}/adapters/{adapter-id}
```
