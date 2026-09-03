# Adding Failover and Circuit Breakers to an AI Gateway

*This is a technical deep-dive from Week 3 of my 60-day sprint building a production-grade AI Inference Gateway in Go.*

Building a proxy to forward requests to an LLM provider (like OpenAI or Anthropic) is easy. Building a proxy that stays online when those providers crash is a completely different engineering challenge.

Over the past week, I rebuilt the core routing engine of my Go gateway to handle catastrophic upstream failures without the end-user ever noticing. Here is how I layered Exponential Backoff, Circuit Breakers, and Automated Failover into the system.

## 1. The Danger of Naive Retries
When an upstream provider throws a `503 Service Unavailable`, the amateur response is to wrap the call in a `for` loop and retry instantly. 

If you have 1,000 concurrent users and the provider drops, all 1,000 connections will instantly retry at the exact same millisecond. This creates a "thundering herd" that acts like a DDoS attack against your own infrastructure (or the recovering provider).

**The Fix:** I implemented Exponential Backoff with Jitter. 
By multiplying the retry delay and adding randomized jitter (`time.Duration(rand.Float64() * float64(sleepDuration) * 0.5)`), the retries naturally spread out, smoothing the load curve and allowing the upstream breathing room to recover.

## 2. Failing Fast: The Circuit Breaker
Retries are great for intermittent network blips, but what if the upstream provider is completely dead? If your timeout is 5 seconds, and 1,000 requests stack up waiting for that timeout, you will instantly exhaust your gateway's thread pool and crash your own server.

**The Fix:** I hand-rolled a state machine Circuit Breaker (Closed / Open / Half-Open).
If a provider fails 5 times consecutively, the breaker trips to `OPEN`. Any subsequent requests don't even attempt the network hop—they are rejected instantly in memory. 

To make this blazing fast, I benchmarked `sync.Mutex` against `atomic` operations. I implemented an **Atomic Fast-Path** using `atomic.Int32` for the state check. The result? 
Checking the circuit breaker now takes **0.51 nanoseconds**—a 100x speedup over a standard lock. We save our CPU cycles and prevent socket exhaustion.

## 3. Seamless Provider Failover
Failing fast is great for our server, but the user still gets an error. What if we could just route around the fire?

I restructured the proxy to hold a slice of `[]Upstream` targets (e.g., Target 1: OpenAI, Target 2: Anthropic), each with its own independent Circuit Breaker. 

If the primary provider times out or its breaker is `OPEN`, the gateway automatically shifts to the next target in the slice. Because this all happens within the boundaries of a single HTTP request lifecycle, the end-user simply waits an extra second and receives a `200 OK`. They have absolutely no idea that the primary system was on fire.

## 4. The Hidden Trap: Double-Billing
There is one massive trap with automated retries: Network timeouts.
If your gateway sends a prompt, and the network drops before you receive the response, *did the provider process the prompt?* 

If you blindly retry that request against the same provider, they might process it twice, doubling your token cost. 

**The Fix:** Idempotency Keys. 
At the very edge of the gateway, before the retry loop begins, we generate a cryptographically secure `X-Idempotency-Key` and attach it to the request. Every single retry for that specific user request shares the exact same key. If the upstream provider receives a retry for a prompt it already processed, it simply returns the cached response instead of billing us twice.

## Conclusion
Resilience isn't just about trying again—it's about protecting your own infrastructure, protecting the user experience, and protecting your wallet. 

By combining jittered retries, atomic circuit breakers, intelligent routing, and strict idempotency, the gateway transforms from a simple proxy into an enterprise-grade load balancer.
