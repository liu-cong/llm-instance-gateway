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
decisions. The model servers MUST provide the following metrics via a Prometheus endpoint. While
the metric names may differ slightly in different model servers, the metric types MUST be the same.
We will align with the
[model server metrics standardization](https://docs.google.com/document/d/1SpSp1E6moa4HSrJnS4x3NpLuj88sMXr2tbofKlzTZpk)
effort.

We also show the metrics in vLLM, which is already integrated into the inference extension. We will
add more model servers once they are integrated.

| Metric | Type | Description | vLLM metric |
| ----- | ---- | ---- | ---- |
| TotalQueuedRequests         | Gauge     | The current total number of requests in the queue.| `vllm:num_requests_waiting`|
| KVCacheUtilization| Gauge     | The current KV cache utilization in percentage.| `vllm:gpu_cache_usage_perc`|

## [Experimental] LoRA Adapter Serving

Model servers that support dynamic LoRA serving can gain additional benefit from the inference
extension's LoRA affinity algorithm. As dynamic LoRA serving is quite new and evolving, this part is considered experimental and subject to changes in future releases.

The inference extension expects the following behavior from compatible model servers.

* Support running multiple LoRA adapters in parallel in the same decode batch.
* Dynamically load/unload adapters in GPU memory from/to a cache (e.g., in host memory) depending on
  the requested adapters in the current batch.

The model server MUST expose the following LoRA adapter information via a RESTful API with response in JSON :

* `Config` 
  * `LoRAEnabled`: boolean, whether dynamic LoRA serving is enabled.
  *  `MaxActiveAdapter`: integer, the maximum number of adapters that can be loaded to GPU memory to serve a batch.
  Requests will be queued if the model server has reached MaxActiveAdapter and cannot load the
  requested adapter. 
* `State`
  * `ActiveAdapters`: List[string], a list of adapters that are currently loaded in GPU memory and ready to serve
  requests.

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
`MaxActiveAdapters` is exposed as a string label `max_lora`, and `ActiveAdapters` as a comma separated string label `running_lora_adapters`.