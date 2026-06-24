import type { DirEntry } from "./types";

export interface WorkspaceSearchRow {
  path: string;
  entry: DirEntry;
}

function basename(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] || path;
}

export function mergeWorkspaceSearchResults(rows: WorkspaceSearchRow[], results: DirEntry[] | null): WorkspaceSearchRow[] {
  if (!results || results.length === 0) return rows;
  const merged = [...rows];
  const seen = new Set(rows.map((row) => row.path));
  for (const result of results) {
    if (seen.has(result.name)) continue;
    merged.push({ path: result.name, entry: { name: basename(result.name), isDir: result.isDir } });
    seen.add(result.name);
  }
  return merged;
}
