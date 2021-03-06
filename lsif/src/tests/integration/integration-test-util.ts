import * as constants from '../../shared/constants'
import * as fs from 'mz/fs'
import * as nodepath from 'path'
import * as uuid from 'uuid'
import rmfr from 'rmfr'
import * as pgModels from '../../shared/models/pg'
import { Backend } from '../../server/backend/backend'
import { child_process } from 'mz'
import { Connection } from 'typeorm'
import { connectPostgres } from '../../shared/database/postgres'
import { convertLsif } from '../../worker/conversion/importer'
import { dbFilename, ensureDirectory } from '../../shared/paths'
import { lsp } from 'lsif-protocol'
import { userInfo } from 'os'
import { DumpManager } from '../../shared/store/dumps'
import { DependencyManager } from '../../shared/store/dependencies'
import { createSilentLogger } from '../../shared/logging'
import { PathExistenceChecker } from '../../worker/conversion/existence'
import { ReferencePaginationCursor } from '../../server/backend/cursor'
import { isEqual, uniqWith } from 'lodash'
import { InternalLocation } from '../../server/backend/location'

/** Create a temporary directory with a subdirectory for dbs. */
export async function createStorageRoot(): Promise<string> {
    const tempPath = await fs.mkdtemp('test-', { encoding: 'utf8' })
    await ensureDirectory(nodepath.join(tempPath, constants.DBS_DIR))
    return tempPath
}

/**
 * Create a new postgres database with a random suffix, apply the frontend
 * migrations (via the ./dev/migrate.sh script) and return an open connection.
 * This uses the PG* environment variables for host, port, user, and password.
 * This also returns a cleanup function that will destroy the database, which
 * should be called at the end of the test.
 */
export async function createCleanPostgresDatabase(): Promise<{ connection: Connection; cleanup: () => Promise<void> }> {
    // Each test has a random dbname suffix
    const suffix = uuid.v4().substring(0, 8)

    // Pull test db config from environment
    const host = process.env.PGHOST || 'localhost'
    const port = parseInt(process.env.PGPORT || '5432', 10)
    const username = process.env.PGUSER || userInfo().username || 'postgres'
    const password = process.env.PGPASSWORD || ''
    const database = `sourcegraph-test-lsif-${suffix}`

    // Determine the path of the migrate script. This will cover the case
    // where `yarn test` is run from within the root or from the lsif directory.
    // const migrateScriptPath = nodepath.join((await fs.exists('dev')) ? '' : '..', 'dev', 'migrate.sh')
    const migrationsPath = nodepath.join((await fs.exists('migrations')) ? '' : '..', 'migrations')

    // Ensure environment gets passed to child commands
    const env = {
        ...process.env,
        PGHOST: host,
        PGPORT: `${port}`,
        PGUSER: username,
        PGPASSWORD: password,
        PGSSLMODE: 'disable',
        PGDATABASE: database,
    }

    // Construct postgres connection string using environment above. We disable this
    // eslint rule because we want it to use bash interpolation, not typescript string
    // templates.
    //
    // eslint-disable-next-line no-template-curly-in-string
    const connectionString = 'postgres://${PGUSER}:${PGPASSWORD}@${PGHOST}:${PGPORT}/${PGDATABASE}?sslmode=disable'

    // Define command text
    const createCommand = `createdb ${database}`
    const dropCommand = `dropdb --if-exists ${database}`
    const migrateCommand = `migrate -database "${connectionString}" -path  ${migrationsPath} up`

    // Create cleanup function to run after test. This will close the connection
    // created below (if successful), then destroy the database that was created
    // for the test. It is necessary to close the database first, otherwise we
    // get failures during the after hooks:
    //
    // dropdb: database removal failed: ERROR:  database "sourcegraph-test-lsif-5033c9e8" is being accessed by other users

    let connection: Connection
    const cleanup = async (): Promise<void> => {
        if (connection) {
            await connection.close()
        }

        await child_process.exec(dropCommand, { env }).then(() => undefined)
    }

    // Try to create database
    await child_process.exec(createCommand, { env })

    try {
        // Run migrations then connect to database
        await child_process.exec(migrateCommand, { env })
        connection = await connectPostgres(
            { host, port, username, password, database, ssl: false },
            suffix,
            createSilentLogger()
        )
        return { connection, cleanup }
    } catch (error) {
        // We made a database but can't use it - try to clean up
        // before throwing the original error.

        try {
            await cleanup()
        } catch (_) {
            // If a new error occurs, swallow it
        }

        // Throw the original error
        throw error
    }
}

/**
 * Truncate all tables that do not match `schema_migrations`.
 *
 * @param connection The connection to use.
 */
export async function truncatePostgresTables(connection: Connection): Promise<void> {
    const results: { table_name: string }[] = await connection.query(
        "SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name != 'schema_migrations'"
    )

    const tableNames = results.map(row => row.table_name).join(', ')
    await connection.query(`truncate ${tableNames} restart identity`)
}

/**
 * Insert an upload entity and return the corresponding dump entity.
 *
 * @param connection The Postgres connection.
 * @param dumpManager The dumps manager instance.
 * @param repositoryId The repository identifier.
 * @param commit The commit.
 * @param root The root of all files in the dump.
 * @param indexer The type of indexer used to produce this dump.
 */
export async function insertDump(
    connection: Connection,
    dumpManager: DumpManager,
    repositoryId: number,
    commit: string,
    root: string,
    indexer: string
): Promise<pgModels.LsifDump> {
    await dumpManager.deleteOverlappingDumps(repositoryId, commit, root, indexer, {})

    const upload = new pgModels.LsifUpload()
    upload.repositoryId = repositoryId
    upload.commit = commit
    upload.root = root
    upload.indexer = indexer
    upload.filename = '<test>'
    upload.uploadedAt = new Date()
    upload.state = 'completed'
    upload.tracingContext = '{}'
    await connection.createEntityManager().save(upload)

    const dump = new pgModels.LsifDump()
    dump.id = upload.id
    dump.repositoryId = repositoryId
    dump.commit = commit
    dump.root = root
    dump.indexer = indexer
    return dump
}

/**
 * Mock an upload of the given file. This will create a SQLite database in the
 * given storage root and will insert dump, package, and reference data into
 * the given Postgres database.
 *
 * @param connection The Postgres connection.
 * @param dumpManager The dumps manager instance.
 * @param dependencyManager The dependency manager instance.
 * @param storageRoot The temporary storage root.
 * @param repositoryId The repository identifier.
 * @param commit The commit.
 * @param root The root of all files in the dump.
 * @param indexer The indexer that produced the dump.
 * @param filename The filename of the (gzipped) LSIF dump.
 * @param updateCommits Whether not to update commits.
 */
export async function convertTestData(
    connection: Connection,
    dumpManager: DumpManager,
    dependencyManager: DependencyManager,
    storageRoot: string,
    repositoryId: number,
    commit: string,
    root: string,
    indexer: string,
    filename: string,
    updateCommits: boolean = true
): Promise<void> {
    // Create a filesystem read stream for the given test file. This will cover
    // the cases where `yarn test` is run from the root or from the lsif directory.
    const fullFilename = nodepath.join((await fs.exists('lsif')) ? 'lsif' : '', 'src/tests/integration/data', filename)

    const tmp = nodepath.join(storageRoot, constants.TEMP_DIR, uuid.v4())
    const pathExistenceChecker = new PathExistenceChecker({ repositoryId, commit, root })

    const { packages, references } = await convertLsif({
        path: fullFilename,
        root: '',
        database: tmp,
        pathExistenceChecker,
    })

    const dump = await insertDump(connection, dumpManager, repositoryId, commit, root, indexer)
    await dependencyManager.addPackagesAndReferences(dump.id, packages, references)
    await fs.rename(tmp, dbFilename(storageRoot, dump.id))

    if (updateCommits) {
        await dumpManager.updateCommits(
            repositoryId,
            new Map<string, Set<string>>([[commit, new Set<string>()]])
        )
        await dumpManager.updateDumpsVisibleFromTip(repositoryId, commit)
    }
}

/**
 * A wrapper around tests for the Backend class. This abstracts a lot
 * of the common setup and teardown around creating a temporary Postgres
 * database, a storage root, a dumps manager, a dependency manager, and a
 * backend instance.
 */
export class BackendTestContext {
    /** A temporary directory. */
    private storageRoot?: string

    /** The Postgres connection. */
    private connection?: Connection

    /** A reference to a function that destroys the temporary database. */
    private cleanup?: () => Promise<void>

    /** The backend instance. */
    public backend?: Backend

    /** The dumps manager instance. */
    public dumpManager?: DumpManager

    /** The dependency manager instance. */
    public dependencyManager?: DependencyManager

    /**
     * Create a backend, a dumps manager, and a dependency manager instance.
     * This will create temporary resources (database and temporary directory)
     * that should be cleaned up via the `teardown` method.
     *
     * The backend and data manager instances can be referenced by the public
     * fields of this class.
     */
    public async init(): Promise<void> {
        this.storageRoot = await createStorageRoot()
        const { connection, cleanup } = await createCleanPostgresDatabase()
        this.connection = connection
        this.cleanup = cleanup
        this.dumpManager = new DumpManager(connection)
        this.dependencyManager = new DependencyManager(connection)
        this.backend = new Backend(this.storageRoot, this.dumpManager, this.dependencyManager, '')
    }

    /**
     * Mock an upload of the given file. This will create a SQLite database in the
     * given storage root and will insert dump, package, and reference data into
     * the given Postgres database.
     *
     * @param repositoryId The repository identifier.
     * @param commit The commit.
     * @param root The root of all files in the dump.
     * @param indexer The type of indexer used to produce this dump.
     * @param filename The filename of the (gzipped) LSIF dump.
     * @param updateCommits Whether not to update commits.
     */
    public convertTestData(
        repositoryId: number,
        commit: string,
        root: string,
        indexer: string,
        filename: string,
        updateCommits: boolean = true
    ): Promise<void> {
        if (!this.connection || !this.dumpManager || !this.dependencyManager || !this.storageRoot) {
            return Promise.resolve()
        }

        return convertTestData(
            this.connection,
            this.dumpManager,
            this.dependencyManager,
            this.storageRoot,
            repositoryId,
            commit,
            root,
            indexer,
            filename,
            updateCommits
        )
    }

    /** Clean up disk and database resources created for this test. */
    public async teardown(): Promise<void> {
        if (this.storageRoot) {
            await rmfr(this.storageRoot)
        }

        if (this.cleanup) {
            await this.cleanup()
        }
    }
}

/**
 * Create an LSP location with a remote URI.
 *
 * @param repositoryId The repository identifier.
 * @param commit The commit.
 * @param documentPath The document nodepath.
 * @param startLine The starting line.
 * @param startCharacter The starting character.
 * @param endLine The ending line.
 * @param endCharacter The ending character.
 */
export function createLocation(
    repositoryId: number,
    commit: string,
    documentPath: string,
    startLine: number,
    startCharacter: number,
    endLine: number,
    endCharacter: number
): lsp.Location {
    const url = new URL(`git://${repositoryId}`)
    url.search = commit
    url.hash = documentPath

    return lsp.Location.create(url.href, {
        start: {
            line: startLine,
            character: startCharacter,
        },
        end: {
            line: endLine,
            character: endCharacter,
        },
    })
}

/**
 * Map an internal location to an LSP location.
 *
 * @param location The internal location.
 */
export function mapLocation(location: InternalLocation): lsp.Location {
    return createLocation(
        location.dump.repositoryId,
        location.dump.commit,
        location.path,
        location.range.start.line,
        location.range.start.character,
        location.range.end.line,
        location.range.end.character
    )
}

/**
 * Map the locations field from internal locations to LSP locations.
 *
 * @param resp The input containing a locations array.
 */
export function mapLocations<T extends { locations: InternalLocation[] }>(
    resp: T
): Omit<T, 'locations'> & { locations: lsp.Location[] } {
    return {
        ...resp,
        locations: resp.locations.map(mapLocation),
    }
}

/** A counter used for unique commit generation. */
let commitBase = 0

/**
 * Create a 40-character commit.
 *
 * @param base A unique numeric base to repeat.
 */
export function createCommit(base?: number): string {
    if (base === undefined) {
        base = commitBase
        commitBase++
    }

    // Add 'a' to differentiate between similar numeric bases such as `1a1a...` and `11a11a...`.
    return (base + 'a').repeat(40).substring(0, 40)
}

/**
 * Remove all node_modules locations from the output of a references result.
 *
 * @param resp The input containing a locations array.
 */
export function filterNodeModules<T extends { locations: lsp.Location[] }>(resp: T): T {
    return {
        ...resp,
        locations: uniqWith(
            resp.locations.filter(l => !l.uri.includes('node_modules')),
            isEqual
        ),
    }
}

/**
 * Query all pages of references from the given backend.
 *
 * @param backend The backend instance.
 * @param repositoryId The repository identifier.
 * @param commit The commit.
 * @param path The path of the document to which the position belongs.
 * @param position The current hover position.
 * @param limit The page limit.
 * @param remoteDumpLimit The maximum number of remote dumps to query in one operation.
 */
export async function queryAllReferences(
    backend: Backend,
    repositoryId: number,
    commit: string,
    path: string,
    position: lsp.Position,
    limit: number,
    remoteDumpLimit?: number
): Promise<{ locations: InternalLocation[]; pageSizes: number[]; numPages: number }> {
    let locations: InternalLocation[] = []
    const pageSizes: number[] = []
    let cursor: ReferencePaginationCursor | undefined

    while (true) {
        const result = await backend.references(
            repositoryId,
            commit,
            path,
            position,
            { limit, cursor },
            remoteDumpLimit
        )
        if (!result) {
            break
        }

        locations = locations.concat(result.locations)
        pageSizes.push(result.locations.length)

        if (!result.newCursor) {
            break
        }
        cursor = result.newCursor
    }

    return { locations, pageSizes, numPages: pageSizes.length }
}
