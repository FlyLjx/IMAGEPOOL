export type ReleaseInfo = {
  version: string;
  date: string;
  items: { type: string; content: string }[];
};

export const githubRepositoryURL = "https://github.com/FlyLjx/IMAGEPOOL";
export const githubLatestReleaseURL = "https://api.github.com/repos/FlyLjx/IMAGEPOOL/releases/latest";
export const githubChangelogURL = "https://raw.githubusercontent.com/FlyLjx/IMAGEPOOL/main/CHANGELOG.md";

export function parseChangelog(content: string): ReleaseInfo[] {
  return content
    .split(/^## /m)
    .slice(1)
    .map((block) => {
      const [title = "", ...lines] = block.trim().split("\n");
      const [, version = title.trim(), date = ""] =
        title.match(/^(.+?)(?:\s+-\s+(.+))?$/) || [];
      return {
        version: version.trim(),
        date: date.trim(),
        items: lines
          .map((line) => line.trim().match(/^\+\s+\[(.+?)\]\s+(.+)$/))
          .filter((match): match is RegExpMatchArray => Boolean(match))
          .map((match) => ({ type: match[1], content: match[2] })),
      };
    })
    .filter((release) => release.items.length);
}
