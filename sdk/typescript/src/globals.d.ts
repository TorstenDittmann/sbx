// Node and Bun provide process. This keeps the package off @types/node.
declare const process: { env: Record<string, string | undefined> }
