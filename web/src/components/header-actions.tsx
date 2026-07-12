"use client";

import { ThemeToggle } from "@/components/theme-toggle";
import { VersionReleaseDialog } from "@/components/version-release-dialog";
import { githubRepositoryURL } from "@/lib/release";
import { cn } from "@/lib/utils";

export function HeaderActions({ className, showGithubText = true, canUpdate = false }: { className?: string; showGithubText?: boolean; canUpdate?: boolean }) {
  return (
    <div className={cn("flex items-center gap-2 sm:gap-3", className)}>
      <ThemeToggle />
      <a
        href={githubRepositoryURL}
        target="_blank"
        rel="noreferrer"
        className="inline-flex h-8 items-center justify-center gap-1.5 text-sm text-stone-500 transition hover:text-stone-900 dark:text-stone-300 dark:hover:text-white"
        aria-label="GitHub repository"
      >
        <img src="/github.svg" alt="" className="size-4" />
        {showGithubText ? <span className="hidden sm:inline">GitHub</span> : null}
      </a>
      <VersionReleaseDialog canUpdate={canUpdate} />
    </div>
  );
}
