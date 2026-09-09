import { useState, useRef, useEffect } from 'react';
import { Send, Terminal, ShieldAlert, CheckCircle2, Zap, AlertTriangle, ArrowRight } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

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
};

// ----------------------------------------------------------------------------
// App Component
// ----------------------------------------------------------------------------
export default function App() {
  // State
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [logs, setLogs] = useState<TelemetryLog[]>([]);
  const [token, setToken] = useState('');

  // Refs for auto-scrolling
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const logsEndRef = useRef<HTMLDivElement>(null);

  // Auto-scroll
  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs]);

  // Generate a mock JWT on load so they don't get 401s
  useEffect(() => {
    // In a real app, this comes from a login. We just spoof a valid structure
    // so the backend identity middleware doesn't complain (if it requires signatures, 
    // we need to tell the user to paste a real token. Let's provide a text input for it).
  }, []);

  const handleSend = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!input.trim() || !token.trim()) return;

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

    try {
      const response = await fetch('http://localhost:8081/v1/chat/completions', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`,
        },
        body: JSON.stringify({
          model: 'mock-llm-v1',
          messages: [{ role: 'user', content: userMessage.content }],
        }),
      });

      const endTime = performance.now();
      const latencyMs = Math.round(endTime - startTime);

      // Extract Headers
      const exposedHeaders: Record<string, string> = {};
      response.headers.forEach((val, key) => {
        exposedHeaders[key] = val;
      });

      const log: TelemetryLog = {
        id: crypto.randomUUID(),
        timestamp: new Date(),
        method: 'POST',
        url: '/v1/chat/completions',
        status: response.status,
        latencyMs,
        headers: exposedHeaders,
      };

      setLogs((prev) => [...prev, log]);

      if (response.ok) {
        const data = await response.json();
        const assistantMessage: Message = {
          id: crypto.randomUUID(),
          role: 'assistant',
          content: data.choices?.[0]?.message?.content || 'No response',
          timestamp: new Date(),
        };
        setMessages((prev) => [...prev, assistantMessage]);
      } else {
        const errorText = await response.text();
        log.error = errorText;
        setLogs((prev) => {
          const newLogs = [...prev];
          newLogs[newLogs.length - 1] = log;
          return newLogs;
        });
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

  return (
    <div className="min-h-screen bg-neutral-950 flex flex-col font-sans">
      {/* ----------------- Header / Arch Map ----------------- */}
      <header className="border-b border-neutral-800 bg-neutral-900/50 p-4">
        <div className="max-w-7xl mx-auto flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-8 h-8 rounded-lg bg-copper-500 flex items-center justify-center">
              <Zap className="w-5 h-5 text-white" />
            </div>
            <h1 className="text-xl font-medium tracking-tight text-neutral-100">AI Gateway</h1>
          </div>
          
          <div className="flex items-center gap-4 text-sm text-neutral-400">
            <div className="flex items-center gap-2">
              <div className="px-3 py-1.5 rounded-md bg-neutral-800 border border-neutral-700">Client</div>
              <ArrowRight className="w-4 h-4" />
              <div className="px-3 py-1.5 rounded-md bg-neutral-800 border border-neutral-700 flex items-center gap-1.5">
                <ShieldAlert className="w-4 h-4 text-amber-500" />
                Rate Limiter
              </div>
              <ArrowRight className="w-4 h-4" />
              <div className="px-3 py-1.5 rounded-md bg-neutral-800 border border-neutral-700 flex items-center gap-1.5">
                <AlertTriangle className="w-4 h-4 text-rose-500" />
                Circuit Breaker
              </div>
              <ArrowRight className="w-4 h-4" />
              <div className="px-3 py-1.5 rounded-md bg-copper-900/30 border border-copper-700/50 text-copper-400 flex items-center gap-1.5">
                <CheckCircle2 className="w-4 h-4" />
                Upstream
              </div>
            </div>
          </div>
        </div>
      </header>

      {/* ----------------- Main Split View ----------------- */}
      <main className="flex-1 flex overflow-hidden max-w-7xl w-full mx-auto">
        
        {/* Left Pane: Chat Client */}
        <section className="flex-1 flex flex-col border-r border-neutral-800 relative bg-neutral-950/50">
          <div className="p-4 border-b border-neutral-800 bg-neutral-900/30 flex items-center justify-between">
            <h2 className="text-sm font-medium text-neutral-300">Client Application</h2>
            <input
              type="text"
              placeholder="Paste JWT Token..."
              value={token}
              onChange={(e) => setToken(e.target.value)}
              className="px-3 py-1.5 bg-neutral-800 border border-neutral-700 rounded-md text-xs w-64 focus:outline-none focus:border-copper-500 transition-colors"
            />
          </div>

          <div className="flex-1 overflow-y-auto p-6 space-y-6">
            {messages.length === 0 && (
              <div className="h-full flex flex-col items-center justify-center text-neutral-500 space-y-4">
                <Terminal className="w-12 h-12 opacity-20" />
                <p>Send a message to watch the gateway in action.</p>
              </div>
            )}
            
            <AnimatePresence initial={false}>
              {messages.map((msg) => (
                <motion.div
                  key={msg.id}
                  initial={{ opacity: 0, y: 10 }}
                  animate={{ opacity: 1, y: 0 }}
                  className={cn(
                    "flex w-full",
                    msg.role === 'user' ? "justify-end" : "justify-start"
                  )}
                >
                  <div className={cn(
                    "max-w-[80%] rounded-2xl px-5 py-3.5 text-sm leading-relaxed shadow-sm",
                    msg.role === 'user' 
                      ? "bg-copper-600 text-white rounded-br-none" 
                      : "bg-neutral-800 border border-neutral-700 text-neutral-200 rounded-bl-none"
                  )}>
                    {msg.content}
                  </div>
                </motion.div>
              ))}
            </AnimatePresence>
            <div ref={messagesEndRef} />
          </div>

          <div className="p-4 bg-neutral-900/50 border-t border-neutral-800">
            <form onSubmit={handleSend} className="relative flex items-center">
              <input
                type="text"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder={!token ? "Please paste your JWT token first..." : "Type a prompt..."}
                disabled={isLoading || !token}
                className="w-full bg-neutral-800 border border-neutral-700 rounded-xl pl-4 pr-12 py-3.5 text-sm focus:outline-none focus:ring-1 focus:ring-copper-500 disabled:opacity-50 transition-shadow"
              />
              <button
                type="submit"
                disabled={isLoading || !input.trim() || !token}
                className="absolute right-2 p-2 rounded-lg bg-copper-600 hover:bg-copper-500 disabled:bg-neutral-700 text-white transition-colors"
              >
                <Send className="w-4 h-4" />
              </button>
            </form>
          </div>
        </section>

        {/* Right Pane: Telemetry Inspector */}
        <section className="w-[450px] flex flex-col bg-[#0d0d0d] relative">
          <div className="p-4 border-b border-neutral-800 flex items-center gap-2">
            <Terminal className="w-4 h-4 text-neutral-400" />
            <h2 className="text-sm font-medium text-neutral-300">Gateway Telemetry</h2>
          </div>

          <div className="flex-1 overflow-y-auto p-4 space-y-4 font-mono text-xs">
            {logs.length === 0 && (
              <div className="text-neutral-600 text-center mt-10">Waiting for requests...</div>
            )}
            
            <AnimatePresence initial={false}>
              {logs.map((log) => (
                <motion.div
                  key={log.id}
                  initial={{ opacity: 0, x: 20 }}
                  animate={{ opacity: 1, x: 0 }}
                  className="bg-neutral-900 border border-neutral-800 rounded-lg p-3 space-y-2"
                >
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <span className="text-blue-400 font-bold">{log.method}</span>
                      <span className="text-neutral-300">{log.url}</span>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-neutral-500">{log.latencyMs}ms</span>
                      <span className={cn(
                        "px-2 py-0.5 rounded font-bold",
                        log.status === 200 ? "bg-emerald-500/10 text-emerald-400" :
                        log.status === 429 ? "bg-amber-500/10 text-amber-400" :
                        "bg-rose-500/10 text-rose-400"
                      )}>
                        {log.status || 'ERR'}
                      </span>
                    </div>
                  </div>

                  {log.error && (
                    <div className="text-rose-400 bg-rose-500/5 p-2 rounded border border-rose-500/10">
                      {log.error}
                    </div>
                  )}

                  <div className="pt-2 border-t border-neutral-800 space-y-1">
                    <div className="text-neutral-500 mb-1">Response Headers:</div>
                    {Object.entries(log.headers).map(([k, v]) => {
                      if (!['x-request-id', 'x-mock-upstream', 'retry-after'].includes(k.toLowerCase())) return null;
                      return (
                        <div key={k} className="flex">
                          <span className="text-copper-400 w-32 shrink-0">{k}:</span>
                          <span className="text-neutral-300 truncate">{v}</span>
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
