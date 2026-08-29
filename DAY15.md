# Day 15 — Exponential Backoff & Jitter

**Concept Mastered:** Handling upstream failure using retries, exponential backoff, and avoiding the "Thundering Herd" problem using jitter.
**Built:** Modified the core Reverse Proxy (`internal/proxy/proxy.go`) to automatically buffer the HTTP request and retry upstream failures up to 3 times before returning an error to the user.

---

## 1. The Scenario: Network Flakes and Throttling
We rely on an upstream provider (like OpenAI or Anthropic) to answer the user's queries. But networks are flaky. 
Sometimes the provider returns a `502 Bad Gateway`, a `503 Service Unavailable`, or a `429 Too Many Requests`.
Before today, if that happened, our Gateway simply threw its hands up and returned a 500-level error to the user.

Today, we built a **Retry Loop** to handle failures gracefully without the client ever knowing something went wrong.

## 2. The Naive Solution (And Why It Fails)
The simplest way to retry is just a `for` loop:
```go
for i := 0; i < 3; i++ {
    resp, err := http.Get("https://api.openai.com")
    if err == nil { return resp }
}
```
**Why this is dangerous:** If OpenAI goes down for 2 seconds, and we have 5,000 concurrent users trying to talk to OpenAI, all 5,000 of our goroutines will instantly retry in a tight loop. We will inadvertently execute a Denial of Service (DoS) attack against our provider, likely getting our API key permanently banned.

## 3. The Expert Solution: Exponential Backoff + Jitter

To solve this, we introduce **Exponential Backoff**:
- 1st Failure: Wait 100ms
- 2nd Failure: Wait 200ms
- 3rd Failure: Wait 400ms

**The Thundering Herd Problem:**
But wait! If OpenAI goes down precisely at 12:00:00, all 5,000 of our concurrent users will fail at the exact same millisecond. 
Even with exponential backoff, all 5,000 users will wait exactly 100ms and retry at 12:00:00.100. Then they will all wait 200ms and retry at 12:00:00.300. 
They act like a "thundering herd", stampeding the provider in massive, synchronized waves.

**The Fix: Jitter (Randomness)**
We fix the thundering herd by injecting **Jitter**. Jitter is just a fancy word for randomness. 
Instead of waiting exactly 100ms, we wait `100ms + (up to 50% random jitter)`.
- User A might wait 112ms.
- User B might wait 145ms.
- User C might wait 101ms.

This tiny bit of math (`jitter := time.Duration(rand.Float64() * float64(sleepDuration) * 0.5)`) completely smooths out the massive spikes, distributing the retry load evenly over time and saving both our servers and the upstream provider from catastrophic failure.

## 4. Reading the Code (`internal/proxy/proxy.go`)

1. **Buffering the Body:** Because HTTP request bodies (`r.Body`) are streams that can only be read once, we read the JSON payload into a `[]byte` slice first. This allows us to "rewind" and replay the body on each retry attempt.
2. **The Loop:** We loop up to `maxRetries` (3).
3. **The Check:** We check if `err != nil` (network failure) OR `StatusCode >= 500` (provider failure) OR `StatusCode == 429` (rate limited). 
4. **The Backoff:** We calculate `sleepDuration = backoff + jitter`, log a warning, and pause execution using `time.After`.
5. **Context Awareness:** If our internal `ctx.Done()` fires while we are sleeping (meaning the user disconnected or our hard timeout was hit), we immediately break out of the retry loop and clean up.

## 5. What to say out loud in 60 seconds

*"Today I fortified the API Gateway's reliability by implementing an exponential backoff and jitter retry mechanism. Previously, upstream HTTP failures would instantly cascade to the end user. I rewrote the core proxy handler to buffer the request payload in memory, allowing it to replay failed requests up to 3 times. Instead of a naive retry loop which can cause a 'thundering herd' DDoS effect when a provider recovers from an outage, I implemented randomized jitter. This ensures that concurrent retries are smoothly distributed over time, drastically improving our resilience to transient network failures without overloading the upstream."*
