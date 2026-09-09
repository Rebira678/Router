import { useState, useRef, useEffect } from 'react';
import { Send, Terminal, ShieldAlert, CheckCircle2, Zap, AlertTriangle, ArrowRight, ToggleLeft, ToggleRight, ServerCrash, Bot, User } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';
import * as jose from 'jose';

function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// ----------------------------------------------------------------------------
// Types
// ----------------------------------------------------------------------------
type Message = {
  id: string;
  role: 'user' | 'assistant';
  content: string;
  timestamp: Date;
};

type TelemetryLog = {
  id: string;
  timestamp: Date;
  method: string;
  url: string;
  status: number;
  latencyMs: number;
  headers: Record<string, string>;
  error?: string;
  isFailover?: boolean;
};

// ----------------------------------------------------------------------------
// App Component
// ----------------------------------------------------------------------------
export default function App() {
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [logs, setLogs] = useState<TelemetryLog[]>([]);
  const [token, setToken] = useState('');
  const [simulateFailure, setSimulateFailure] = useState(false);

  const messagesEndRef = useRef<HTMLDivElement>(null);
  const logsEndRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages, isLoading]);

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs]);

  // Generate a mock JWT automatically on load for seamless UX
  useEffect(() => {
    const generateToken = async () => {
      const secret = new TextEncoder().encode('super-secret-local-dev-key');
      const jwt = await new jose.SignJWT({ 'sub': 'test-tenant' })
        .setProtectedHeader({ alg: 'HS256' })
        .setIssuedAt()
        .setExpirationTime('30d')
        .sign(secret);
      setToken(jwt);
    };
    generateToken();
  }, []);

  const handleSend = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!input.trim() || !token) return;

    const userMessage: Message = {
      id: crypto.randomUUID(),
      role: 'user',
      content: input,
      timestamp: new Date(),
    };

    setMessages((prev) => [...prev, userMessage]);
    setInput('');
    setIsLoading(true);

    const startTime = performance.now();
    
    // If simulating failure, we pass a header that makes the mock LLM sleep for 6000ms.
    // The Router's hard timeout is 5000ms. This will trigger a context deadline exceeded,
    // trip the circuit breaker, and route to the secondary upstream.
    const reqHeaders: Record<string, string> = {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    };
    if (simulateFailure) {
      reqHeaders['X-Mock-Delay-Ms'] = '6000';
    }

    try {
      const response = await fetch('http://localhost:8081/v1/chat/completions', {
        method: 'POST',
        headers: reqHeaders,
        body: JSON.stringify({
          model: 'mock-llm-v1',
          messages: [{ role: 'user', content: userMessage.content }],
        }),
      });

      const endTime = performance.now();
      const latencyMs = Math.round(endTime - startTime);

      const exposedHeaders: Record<string, string> = {};
      response.headers.forEach((val, key) => {
        exposedHeaders[key] = val;
      });

      const isFailover = exposedHeaders['x-mock-upstream'] === 'mock-secondary';

      const log: TelemetryLog = {
        id: crypto.randomUUID(),
        timestamp: new Date(),
        method: 'POST',
        url: '/v1/chat/completions',
        status: response.status,
        latencyMs,
        headers: exposedHeaders,
        isFailover,
      };

      if (response.ok) {
        const data = await response.json();
        setLogs((prev) => [...prev, log]);
        
        const content = typeof data.choices?.[0]?.message === 'string' 
          ? data.choices[0].message 
          : (data.choices?.[0]?.message?.content || 'No response');

        const assistantMessage: Message = {
          id: crypto.randomUUID(),
          role: 'assistant',
          content: content,
          timestamp: new Date(),
        };
        setMessages((prev) => [...prev, assistantMessage]);
      } else {
        const errorText = await response.text();
        log.error = errorText;
        setLogs((prev) => [...prev, log]);
      }
    } catch (err) {
      const endTime = performance.now();
      setLogs((prev) => [
        ...prev,
        {
          id: crypto.randomUUID(),
          timestamp: new Date(),
          method: 'POST',
          url: '/v1/chat/completions',
          status: 0,
          latencyMs: Math.round(endTime - startTime),
          headers: {},
          error: err instanceof Error ? err.message : 'Network Error',
        },
      ]);
    } finally {
      setIsLoading(false);
    }
  };

  const handleSpam = async () => {
    if (!token) return;
    
    // Fire 15 requests instantly in parallel to guarantee we hit the Rate Limiter (Capacity: 10)
    const promises = Array.from({ length: 15 }).map(async (_, i) => {
      const startTime = performance.now();
      try {
        const response = await fetch('http://localhost:8081/v1/chat/completions', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`,
          },
          body: JSON.stringify({
            model: 'mock-llm-v1',
            messages: [{ role: 'user', content: `Spam message ${i}` }],
          }),
        });
        
        const endTime = performance.now();
        const exposedHeaders: Record<string, string> = {};
        response.headers.forEach((val, key) => { exposedHeaders[key] = val; });

        setLogs((prev) => [...prev, {
          id: crypto.randomUUID(),
          timestamp: new Date(),
          method: 'POST',
          url: '/v1/chat/completions',
          status: response.status,
          latencyMs: Math.round(endTime - startTime),
          headers: exposedHeaders,
        }]);
      } catch (err) {
        // ignore network errors for spam test
      }
    });

    await Promise.all(promises);
  };

  return (
    <div className="min-h-screen bg-[#0a0a0a] flex flex-col font-sans text-neutral-200">
      
      {/* ----------------- Header / Arch Map ----------------- */}
      <header className="border-b border-neutral-800/60 bg-[#111] p-4 sticky top-0 z-10 shadow-sm">
        <div className="max-w-[1400px] mx-auto flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-9 h-9 rounded-xl bg-gradient-to-br from-copper-500 to-copper-700 flex items-center justify-center shadow-lg shadow-copper-900/20">
              <Zap className="w-5 h-5 text-white fill-white/20" />
            </div>
            <div>
              <h1 className="text-lg font-semibold tracking-tight text-white leading-tight">AI Gateway</h1>
              <div className="text-xs text-copper-400 font-medium">Production Visualizer</div>
            </div>
          </div>
          
          <div className="flex items-center gap-3 text-sm">
            <div className="px-3 py-1.5 rounded-lg bg-neutral-900 border border-neutral-800 font-medium text-neutral-400">Client</div>
            <ArrowRight className="w-4 h-4 text-neutral-600" />
            
            <div className="px-3 py-1.5 rounded-lg bg-neutral-900 border border-neutral-800 flex items-center gap-2 font-medium">
              <ShieldAlert className="w-4 h-4 text-amber-500" />
              Rate Limiter
            </div>
            <ArrowRight className="w-4 h-4 text-neutral-600" />
            
            <div className={cn(
              "px-3 py-1.5 rounded-lg border flex items-center gap-2 font-medium transition-colors duration-500",
              simulateFailure ? "bg-rose-950/30 border-rose-900/50 text-rose-400" : "bg-neutral-900 border-neutral-800"
            )}>
              <AlertTriangle className={cn("w-4 h-4", simulateFailure ? "text-rose-500" : "text-neutral-500")} />
              Circuit Breaker
            </div>
            <ArrowRight className="w-4 h-4 text-neutral-600" />
            
            <div className="flex flex-col gap-1">
              <div className={cn(
                "px-3 py-1.5 rounded-lg border text-xs flex items-center gap-2 font-medium transition-all duration-500",
                simulateFailure ? "bg-neutral-900/50 border-neutral-800 text-neutral-600 line-through opacity-50" : "bg-emerald-950/30 border-emerald-900/50 text-emerald-400"
              )}>
                <CheckCircle2 className="w-3.5 h-3.5" />
                Primary LLM
              </div>
              <div className={cn(
                "px-3 py-1.5 rounded-lg border text-xs flex items-center gap-2 font-medium transition-all duration-500",
                simulateFailure ? "bg-amber-950/30 border-amber-900/50 text-amber-400 shadow-[0_0_15px_rgba(245,158,11,0.1)]" : "bg-neutral-900/50 border-neutral-800 text-neutral-600"
              )}>
                <ServerCrash className="w-3.5 h-3.5" />
                Failover LLM
              </div>
            </div>
          </div>
        </div>
      </header>

      {/* ----------------- Main Split View ----------------- */}
      <main className="flex-1 flex overflow-hidden max-w-[1400px] w-full mx-auto">
        
        {/* Left Pane: Chat Client */}
        <section className="flex-1 flex flex-col border-r border-neutral-800/60 relative bg-[#0a0a0a]">
          
          <div className="p-4 border-b border-neutral-800/60 bg-[#111] flex items-center justify-between">
            <h2 className="text-sm font-semibold text-neutral-200">Chat Application</h2>
            
            <div className="flex items-center gap-3">
              <button 
                onClick={handleSpam}
                className="flex items-center gap-2 px-3 py-1.5 rounded-md text-xs font-medium bg-neutral-900 hover:bg-neutral-800 text-amber-500 border border-amber-900/50 transition-all"
              >
                <Zap className="w-4 h-4" />
                Spam Requests (Test 429)
              </button>

              <button 
                onClick={() => setSimulateFailure(!simulateFailure)}
                className={cn(
                  "flex items-center gap-2 px-3 py-1.5 rounded-md text-xs font-medium transition-all",
                  simulateFailure ? "bg-rose-950/40 text-rose-400 border border-rose-900/50" : "bg-neutral-900 hover:bg-neutral-800 text-neutral-400 border border-neutral-800"
                )}
              >
                {simulateFailure ? <ToggleRight className="w-4 h-4" /> : <ToggleLeft className="w-4 h-4" />}
                Simulate Upstream Failure
              </button>
            </div>
          </div>

          <div className="flex-1 overflow-y-auto p-6 space-y-6">
            {messages.length === 0 && (
              <div className="h-full flex flex-col items-center justify-center text-neutral-600 space-y-4">
                <Bot className="w-12 h-12 opacity-20" />
                <p className="text-sm">Send a message to see how the proxy handles it.</p>
              </div>
            )}
            
            <AnimatePresence initial={false}>
              {messages.map((msg) => (
                <motion.div
                  key={msg.id}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  className={cn(
                    "flex w-full gap-3",
                    msg.role === 'user' ? "justify-end" : "justify-start"
                  )}
                >
                  {msg.role === 'assistant' && (
                    <div className="w-8 h-8 shrink-0 rounded-full bg-copper-900/30 border border-copper-700/50 flex items-center justify-center">
                      <Bot className="w-4 h-4 text-copper-400" />
                    </div>
                  )}
                  
                  <div className={cn(
                    "max-w-[75%] rounded-2xl px-4 py-3 text-sm leading-relaxed shadow-sm",
                    msg.role === 'user' 
                      ? "bg-copper-600 text-white rounded-tr-sm" 
                      : "bg-[#161616] border border-neutral-800 text-neutral-300 rounded-tl-sm"
                  )}>
                    {msg.content}
                  </div>

                  {msg.role === 'user' && (
                    <div className="w-8 h-8 shrink-0 rounded-full bg-neutral-800 flex items-center justify-center">
                      <User className="w-4 h-4 text-neutral-400" />
                    </div>
                  )}
                </motion.div>
              ))}
              
              {isLoading && (
                <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="flex justify-start gap-3">
                  <div className="w-8 h-8 shrink-0 rounded-full bg-copper-900/30 border border-copper-700/50 flex items-center justify-center">
                    <Bot className="w-4 h-4 text-copper-400" />
                  </div>
                  <div className="bg-[#161616] border border-neutral-800 rounded-2xl rounded-tl-sm px-4 py-3 flex items-center gap-1.5">
                    <div className="w-1.5 h-1.5 bg-copper-500 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
                    <div className="w-1.5 h-1.5 bg-copper-500 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
                    <div className="w-1.5 h-1.5 bg-copper-500 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
                  </div>
                </motion.div>
              )}
            </AnimatePresence>
            <div ref={messagesEndRef} />
          </div>

          <div className="p-4 bg-[#111] border-t border-neutral-800/60">
            <form onSubmit={handleSend} className="relative flex items-center">
              <input
                type="text"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder="Type a prompt to the AI..."
                disabled={isLoading || !token}
                className="w-full bg-[#0a0a0a] border border-neutral-800 hover:border-neutral-700 rounded-xl pl-4 pr-12 py-3.5 text-sm focus:outline-none focus:border-copper-500 focus:ring-1 focus:ring-copper-500/50 disabled:opacity-50 transition-all placeholder:text-neutral-600"
              />
              <button
                type="submit"
                disabled={isLoading || !input.trim() || !token}
                className="absolute right-2 p-2 rounded-lg bg-copper-600 hover:bg-copper-500 disabled:bg-neutral-800 disabled:text-neutral-600 text-white transition-colors"
              >
                <Send className="w-4 h-4" />
              </button>
            </form>
          </div>
        </section>

        {/* Right Pane: Telemetry Inspector */}
        <section className="w-[450px] flex flex-col bg-[#050505] relative border-l border-neutral-900">
          <div className="p-4 border-b border-neutral-800/60 bg-[#111] flex items-center gap-2">
            <Terminal className="w-4 h-4 text-neutral-400" />
            <h2 className="text-sm font-semibold text-neutral-200">Gateway Telemetry</h2>
          </div>

          <div className="flex-1 overflow-y-auto p-4 space-y-4 font-mono text-[11px] leading-relaxed">
            {logs.length === 0 && (
              <div className="text-neutral-600 text-center mt-10">Listening for HTTP requests...</div>
            )}
            
            <AnimatePresence initial={false}>
              {logs.map((log) => (
                <motion.div
                  key={log.id}
                  initial={{ opacity: 0, x: 20 }}
                  animate={{ opacity: 1, x: 0 }}
                  className={cn(
                    "rounded-lg p-3 space-y-2 border relative overflow-hidden",
                    log.status === 200 && !log.isFailover ? "bg-[#111] border-neutral-800" :
                    log.isFailover ? "bg-amber-950/10 border-amber-900/30" :
                    "bg-rose-950/10 border-rose-900/30"
                  )}
                >
                  {log.isFailover && (
                    <div className="absolute top-0 left-0 w-1 h-full bg-amber-500/50" />
                  )}
                  {log.status === 429 && (
                    <div className="absolute top-0 left-0 w-1 h-full bg-rose-500/50" />
                  )}

                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className="text-blue-400 font-bold">{log.method}</span>
                      <span className="text-neutral-300">{log.url}</span>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className={cn(
                        "font-medium",
                        log.latencyMs > 5000 ? "text-amber-500" : "text-neutral-500"
                      )}>{log.latencyMs}ms</span>
                      <span className={cn(
                        "px-2 py-0.5 rounded font-bold tracking-wider",
                        log.status === 200 ? "bg-emerald-500/10 text-emerald-400" :
                        log.status === 429 ? "bg-rose-500/10 text-rose-400" :
                        "bg-rose-500/10 text-rose-400"
                      )}>
                        {log.status || 'ERR'}
                      </span>
                    </div>
                  </div>

                  {log.isFailover && (
                    <div className="text-amber-400/90 bg-amber-500/5 px-2 py-1.5 rounded-md border border-amber-500/20 flex items-center gap-2">
                      <ServerCrash className="w-3.5 h-3.5" />
                      Failover triggered (Primary Upstream Timeout)
                    </div>
                  )}

                  {log.error && (
                    <div className="text-rose-400 bg-rose-500/5 px-2 py-1.5 rounded-md border border-rose-500/20">
                      {log.error}
                    </div>
                  )}

                  <div className="pt-2 border-t border-neutral-800/50 space-y-1">
                    <div className="text-neutral-600 mb-1">Response Headers:</div>
                    {Object.entries(log.headers).map(([k, v]) => {
                      if (!['x-request-id', 'x-mock-upstream', 'retry-after'].includes(k.toLowerCase())) return null;
                      return (
                        <div key={k} className="flex">
                          <span className="text-copper-400/80 w-32 shrink-0">{k}:</span>
                          <span className={cn(
                            "truncate",
                            k.toLowerCase() === 'x-mock-upstream' && v === 'mock-secondary' ? 'text-amber-400 font-medium' : 'text-neutral-400'
                          )}>{v}</span>
                        </div>
                      );
                    })}
                  </div>
                </motion.div>
              ))}
            </AnimatePresence>
            <div ref={logsEndRef} />
          </div>
        </section>

      </main>
    </div>
  );
}
