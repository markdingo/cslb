## TODO List for cslb

A dumping ground for unresolved issues and discussion topics.

### Health Check Interface

Cslb only knows about network connections, not application success. It may be that an application
could communicate application success or otherwise via a call back into cslb. This would help cslb
make better choices over which targets to use. The main difficult is correlating the application's
URL with the actually target causing the problem. Even a single-threaded application is tricky due
to connection caching by net/http.

```
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != http.StatusOk {
	        cslb.Failed(url)
	}
```

Seems obvious, but which target did cslb use?

### Re-fetch active SRVs

To avoid adding DNS delays to application requests, cslb could re-fetch active SRVs in anticipation
of their reuse. Triggering a fetch, say, five seconds or so before expiry should do the trick.

### Placing a weight in the health-check response

Weights in SRV are obviously a relatively static value. While a GSLB can be used to selectively
respond to SRV queries a more refined approach might be to have the health-check return some sort of
structured content (such as json or xml) containing a utilization value which biases SRV
weights. For example if 2 out of 3 targets are returning 50% utilization and the third is returning
1% utilization, cslb could bias more connections toward the third server.

### Different algorithms beyond weight?

Server-side load-balancers have traditionally offered a variety of different selection algorithms,
such as least-load, least-latency, round-robin, least-connections and more. In the clients case we
can also add network latency as a desirable attribute to consider. Could some of these algorithms be
incorporated into cslb? A service operator could communicate a preferred strategy by, e.g., encoding
algorithm information into weights using an unlikely signal value.

Purpose   | Bits | Value(s)
--------- | ---- | --------
Weight    |  8   | Normal weight ranges from 0-255
Signal    |  5   | 0x1F (all ones)
Algorithm |  3   | 0 = RR, 1 = Latency, 2 = Least Connections, ... 8 = Last

Given three servers: s1, s2, s3 with a weight of 10, 15 and 20 respectively, the 16-bit value of the
weights would then look like:

Server  | Calculation | Encoded Weight | Ratios
------- | ----------- | ----- | ------
s1      | 10 << 8 + 0x1F << 3 + 0 | = 2808 | 10.0
s2      | 15 << 8 + 0x1F << 3 + 0 | = 4088 | 14.6
s3      | 20 << 8 + 0x1F << 3 + 0 | = 5368 | 19.1

The rationale for having the original weight in the top eight bits as opposed to the more obvious
bottom eight bits is that implementations that do not understand this encoding will still work as
the encoded weights still approximate the same ratios as the original values.


### ------
