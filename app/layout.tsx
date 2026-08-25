import { Analytics } from "@vercel/analytics/next";
import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import Header from "@/components/Header";
import { ClimateProvider } from "@/lib/ClimateContext";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  title: "Heatwave Monitor — Climate Intelligence Dashboard",
  description:
    "Real-time heatwave monitoring, heat risk analytics, and early warning forecasts powered by client-side climate intelligence.",
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f8fafc" },
    { media: "(prefers-color-scheme: dark)", color: "#0b0f17" },
  ],
};

const THEME_INIT_SCRIPT = `(function(){try{var t=localStorage.getItem("heatwave-theme");if(!t){t=window.matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light"}document.documentElement.setAttribute("data-theme",t)}catch(e){}})()`;

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      data-theme="dark"
      suppressHydrationWarning
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_INIT_SCRIPT }} />
      </head>
      <body className="min-h-full flex flex-col bg-background text-foreground">
        <ClimateProvider>
          <Header />
          <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col gap-5 px-4 py-6 sm:px-6 lg:px-8">{children}</main>
          <footer className="mx-auto w-full max-w-7xl px-4 py-6 text-center text-xs text-muted sm:px-6 lg:px-8">
            Climate data from Open-Meteo. All analytics computed locally in your browser.
          </footer>
        </ClimateProvider>
        <Analytics />
      </body>
    </html>
  );
}
