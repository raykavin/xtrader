import { createContext, type ReactNode, useContext, useEffect, useState } from "react";
import { fetchSessionInfo, loginPath } from "../api/client";
import { type Access, accessOf, can, NO_ACCESS } from "../lib/access";

/** The access of the user of the session, read once it is known; null while it is read. */
const AccessContext = createContext<Access | null>(null);

/**
 * Reads the session of the dashboard and gives its access to the page: again
 * when the window comes back to the front, the role of the user changed
 * meanwhile. Without a session, the login.
 */
export function AccessProvider({ children }: { children: ReactNode }) {
  const [access, setAccess] = useState<Access | null>(null);

  useEffect(() => {
    let controller = new AbortController();
    const read = () => {
      controller.abort();
      controller = new AbortController();
      fetchSessionInfo(controller.signal)
        .then((session) => {
          if (!session.authenticated) {
            window.location.assign(loginPath());
            return;
          }
          setAccess(accessOf(session));
        })
        .catch(() => {
          // offline: what was read stays; the page tells its own errors
          setAccess((known) => known ?? NO_ACCESS);
        });
    };
    read();
    const onFocus = () => {
      if (document.visibilityState === "visible") read();
    };
    document.addEventListener("visibilitychange", onFocus);
    return () => {
      controller.abort();
      document.removeEventListener("visibilitychange", onFocus);
    };
  }, []);

  return <AccessContext.Provider value={access}>{children}</AccessContext.Provider>;
}

/** The access of the user, null while the session is read. */
export function useAccessState(): Access | null {
  return useContext(AccessContext);
}

/** The access of the user: nothing allowed until known. */
export function useAccess(): Access {
  return useContext(AccessContext) ?? NO_ACCESS;
}

/** Whether the user may any of perms: never until its access is known. */
export function useCan(...perms: string[]): boolean {
  return can(useAccess(), ...perms);
}
