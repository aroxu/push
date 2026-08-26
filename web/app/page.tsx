"use client";

import { useCallback, useRef, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody } from "@heroui/card";
import { Progress } from "@heroui/progress";
import { Snippet } from "@heroui/snippet";
import { Chip } from "@heroui/chip";

type Uploaded = {
  name: string;
  url: string;
  size: number;
  expiresAt: string;
  deleteToken: string;
};

function humanSize(n: number) {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
}

export default function Home() {
  const [dragging, setDragging] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [rate, setRate] = useState<string>("");
  const [result, setResult] = useState<Uploaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const xhrRef = useRef<XMLHttpRequest | null>(null);

  const upload = useCallback((file: File) => {
    setError(null);
    setResult(null);
    setProgress(0);

    const started = Date.now();
    const form = new FormData();
    form.append("file", file, file.name);

    const xhr = new XMLHttpRequest();
    xhrRef.current = xhr;
    xhr.open("POST", "/", true);
    xhr.setRequestHeader("Accept", "application/json");

    xhr.upload.onprogress = (e) => {
      if (!e.lengthComputable) return;
      setProgress(Math.round((e.loaded / e.total) * 100));
      const secs = (Date.now() - started) / 1000;
      if (secs > 0.4) setRate(`${humanSize(e.loaded / secs)}/s`);
    };

    xhr.onload = () => {
      setProgress(null);
      setRate("");
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          const j = JSON.parse(xhr.responseText);
          setResult({
            name: j.filename,
            url: j.url,
            size: j.size,
            expiresAt: j.expires_at,
            deleteToken: j.delete_token,
          });
        } catch {
          setError("Unexpected server response.");
        }
      } else {
        let msg = `Upload failed (${xhr.status}).`;
        try {
          const j = JSON.parse(xhr.responseText);
          if (j.error) msg = j.error;
        } catch {
          /* keep default */
        }
        setError(msg);
      }
    };

    xhr.onerror = () => {
      setProgress(null);
      setError("Network error during upload.");
    };

    xhr.send(form);
  }, []);

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      setDragging(false);
      const file = e.dataTransfer.files?.[0];
      if (file) upload(file);
    },
    [upload],
  );

  const busy = progress !== null;

  return (
    <main className="accent-glow flex min-h-screen flex-col items-center justify-center px-6 py-16">
      <div className="w-full max-w-xl">
        <header className="mb-10 text-center">
          <h1 className="text-5xl font-semibold tracking-tight text-accent">push</h1>
          <p className="mt-3 text-sm text-neutral-400">
            Drop a file, get a link. Everything is deleted after 24 hours.
          </p>
        </header>

        <div
          role="button"
          tabIndex={0}
          aria-label="Drop a file here or click to choose one"
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={onDrop}
          onClick={() => !busy && inputRef.current?.click()}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === " ") inputRef.current?.click();
          }}
          className={`flex cursor-pointer flex-col items-center justify-center rounded-2xl border-2 border-dashed px-8 py-16 transition-colors ${
            dragging
              ? "border-accent bg-accent-500/10"
              : "border-neutral-800 bg-neutral-950/40 hover:border-accent-700"
          }`}
        >
          <input
            ref={inputRef}
            type="file"
            className="hidden"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) upload(f);
              e.target.value = "";
            }}
          />

          {busy ? (
            <div className="w-full max-w-sm text-center">
              <Progress
                aria-label="Upload progress"
                value={progress ?? 0}
                color="primary"
                className="mb-3"
              />
              <p className="font-mono text-sm text-neutral-400">
                {progress}% {rate && <span className="text-neutral-600">· {rate}</span>}
              </p>
              <Button
                size="sm"
                variant="light"
                className="mt-4 text-neutral-500"
                onPress={() => {
                  xhrRef.current?.abort();
                  setProgress(null);
                }}
              >
                Cancel
              </Button>
            </div>
          ) : (
            <>
              <p className="text-lg text-neutral-300">Drag a file here</p>
              <p className="my-3 text-xs uppercase tracking-widest text-neutral-600">or</p>
              <Button color="primary" variant="flat" onPress={() => inputRef.current?.click()}>
                Choose a file
              </Button>
            </>
          )}
        </div>

        <div className="mt-8 text-center">
          <p className="mb-3 text-xs uppercase tracking-widest text-neutral-600">or use curl</p>
          <Snippet
            hideSymbol
            variant="bordered"
            className="w-full justify-between border-neutral-800 bg-neutral-950 font-mono text-xs text-neutral-300"
            classNames={{ pre: "whitespace-pre-wrap break-all text-left" }}
          >
            {`curl -T file.jpg ${typeof window === "undefined" ? "localhost:3234" : window.location.host}`}
          </Snippet>
        </div>

        {error && (
          <Card className="mt-6 border border-red-900/60 bg-red-950/30" shadow="none">
            <CardBody className="text-sm text-red-300">{error}</CardBody>
          </Card>
        )}

        {result && (
          <Card className="mt-6 border border-neutral-800 bg-neutral-950" shadow="none">
            <CardBody className="gap-4">
              <div className="flex items-center justify-between gap-3">
                <span className="truncate text-sm text-neutral-300">{result.name}</span>
                <Chip size="sm" variant="flat" color="primary">
                  {humanSize(result.size)}
                </Chip>
              </div>

              <Snippet
                hideSymbol
                variant="bordered"
                className="w-full border-neutral-800 bg-black font-mono text-xs"
                classNames={{ pre: "whitespace-pre-wrap break-all text-left text-accent" }}
              >
                {result.url}
              </Snippet>

              <p className="text-xs text-neutral-500">
                Expires {new Date(result.expiresAt).toLocaleString()} · delete early with{" "}
                <code className="text-neutral-400">
                  curl -X DELETE {result.url}?token={result.deleteToken.slice(0, 6)}…
                </code>
              </p>
            </CardBody>
          </Card>
        )}

        <footer className="mt-12 text-center text-xs text-neutral-700">
          max 32 GiB · 24h retention · no account needed
        </footer>
      </div>
    </main>
  );
}
