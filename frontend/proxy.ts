import { NextResponse, type NextRequest } from "next/server";

// Forwards /api/v1/* to the backend API gateway, so the browser talks to one origin
// (no CORS, and the gateway's address never reaches the page).
//
// GATEWAY_URL is read on every request, not at build time, so one built image works with
// any gateway address. It must be an origin such as http://gateway:8080. Without it the
// app has no backend and runs in local mode: the client sees "backend_not_configured"
// and computes everything in the browser, exactly as it did before the backend existed.

function gatewayOrigin(): URL | null {
  const raw = process.env.GATEWAY_URL;
  if (!raw) return null;
  try {
    const url = new URL(raw);
    return url.protocol === "http:" || url.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

export function proxy(request: NextRequest) {
  const gateway = gatewayOrigin();
  if (!gateway) {
    return NextResponse.json(
      { error: { code: "backend_not_configured", message: "No backend is configured for this deployment." } },
      { status: 503 },
    );
  }
  return NextResponse.rewrite(new URL(request.nextUrl.pathname + request.nextUrl.search, gateway));
}

export const config = {
  matcher: "/api/v1/:path*",
};
