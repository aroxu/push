import type { Metadata } from "next";
import "./globals.css";
import { Providers } from "./providers";

export const metadata: Metadata = {
  title: "push — drop a file, get a link",
  description:
    "Upload files with a single curl command. Every file self-destructs after 24 hours.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className="dark">
      <body className="min-h-screen bg-canvas antialiased">
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}

