#include <sqlite3.h>
#include <mosquitto/libmosquitto.h>
#include <mosquitto/libcommon.h>
#include "sqlite_paths.h"

#include <array>
#include <chrono>
#include <csignal>
#include <exception>
#include <filesystem>
#include <iostream>
#include <memory>
#include <mutex>
#include <set>
#include <stdexcept>
#include <string>
#include <thread>
#include <vector>

namespace
{
    namespace fs = std::filesystem;
    using Database = std::unique_ptr<sqlite3, decltype(&sqlite3_close)>;
    using Statement = std::unique_ptr<sqlite3_stmt, decltype(&sqlite3_finalize)>;

    volatile std::sig_atomic_t stop_requested = 0;

    void request_stop(int) noexcept
    {
        stop_requested = 1;
    }

    constexpr std::array<const char*, 5> MQTT_TOPICS = 
    {
        "api/home/weather", "voice/home/data",
        "telemetry/home/light/data", "telemetry/home/temperature/data", 
        "journal/home/info"
    };

    void mqtt_check(int result)
    {
        if (result != MOSQ_ERR_SUCCESS) {
            throw std::runtime_error(std::string("MQTT: ") + mosquitto_strerror(result));
        }
    }

    struct MqttLibrary
    {
        MqttLibrary() { mqtt_check(mosquitto_lib_init()); }
        ~MqttLibrary() { mosquitto_lib_cleanup(); }
    };

    struct MqttContext
    {
        std::mutex mutex;
        std::exception_ptr error;
    };

    void mqtt_callback_failed(mosquitto* client, void* userdata) noexcept
    {
        auto& context = *static_cast<MqttContext*>(userdata);
        {
            std::lock_guard<std::mutex> lock(context.mutex);
            context.error = std::current_exception();
        }
        mosquitto_disconnect(client);
    }

    void on_mqtt_message
    (
        mosquitto* client, 
        void* userdata,
        const mosquitto_message* message
    ) noexcept
    {
        try 
        {
            const std::string topic = message->topic;
            const std::string payload = message->payloadlen > 0
                ? std::string(static_cast<const char*>(message->payload), message->payloadlen)
                : std::string();

            if (topic == "api/home/weather") 
            {
                // TODO: разобрать погоду и сохранить в базе api.
                std::cout << "API weather placeholder: " << payload << std::endl;
            } 
            else if (topic == "voice/home/data") 
            {
                // TODO: разобрать распознанный текст и сохранить в базе voice.
                std::cout << "Voice data placeholder: " << payload << std::endl;
            } 
            else if 
            (
                topic == "telemetry/home/light/data" ||
                topic == "telemetry/home/temperature/data"
            ) 
            {
                // TODO: разобрать показание датчика и сохранить в базе telemetry.
                std::cout 
                    << "Telemetry data placeholder [" 
                    << topic 
                    << "]: "
                    << payload 
                << std::endl;
            }
            else if (topic == "journal/home/info")
            {
                // TODO: разобрать журнал и сохранить в базе journal.
                std::cout << "Journal info placeholder: " << payload << std::endl;
            }
        }
        catch (...) 
        {
            mqtt_callback_failed(client, userdata);
        }
    }

    class MqttSubscriber
    {
        // Members outlive the MQTT thread; its shutdown happens in the destructor.
        MqttLibrary library;
        MqttContext context;
        std::unique_ptr<mosquitto, decltype(&mosquitto_destroy)> client{
            nullptr, &mosquitto_destroy
        };

    public:
        MqttSubscriber()
        {
            client.reset(mosquitto_new(nullptr, true, &context));
            if (!client) throw std::runtime_error("Could not create Telemetry MQTT client");

            mosquitto_message_callback_set(client.get(), &on_mqtt_message);
            mosquitto_connect_callback_set(client.get(),
                [](mosquitto* client, void* userdata, int result) noexcept {
                    try {
                        if (result != 0) {
                            throw std::runtime_error("MQTT connection refused: " + std::to_string(result));
                        }
                        // Clean sessions need new subscriptions after every reconnect.
                        for (const char* topic : MQTT_TOPICS) {
                            mqtt_check(mosquitto_subscribe(client, nullptr, topic, 1));
                        }

                        std::cout << "Telemetry MQTT connected; subscriptions requested" << std::endl;
                    }
                    catch (...) {
                        mqtt_callback_failed(client, userdata);
                    }
                });
            mosquitto_subscribe_callback_set(client.get(),
                [](mosquitto* client, void* userdata, int, int count, const int* granted) noexcept {
                    try {
                        for (int i = 0; i < count; ++i) {
                            if (granted[i] >= 128) {
                                throw std::runtime_error("MQTT broker rejected a Telemetry subscription");
                            }
                        }
                    }
                    catch (...) {
                        mqtt_callback_failed(client, userdata);
                    }
                });
            mqtt_check(mosquitto_reconnect_delay_set(client.get(), 1, 30, true));

            std::string host = MOSQUITTO_HOST;
            if (host == "0.0.0.0") host = "127.0.0.1";
            if (host == "::") host = "::1";
            std::cout << "Telemetry MQTT connecting to " << host << ':' << MOSQUITTO_PORT << std::endl;
            mqtt_check(mosquitto_connect(client.get(), host.c_str(), MOSQUITTO_PORT, 60));

            mqtt_check(mosquitto_loop_start(client.get()));
        }

        ~MqttSubscriber()
        {
            mosquitto_disconnect(client.get());
            mosquitto_loop_stop(client.get(), false);
        }

        void rethrow_if_failed()
        {
            std::exception_ptr error;
            {
                std::lock_guard<std::mutex> lock(context.mutex);
                error = context.error;
            }
            if (error) std::rethrow_exception(error);
        }
    };

    struct Column
    {
        const char* name;
        const char* type;
        int primary_key = 0;
    };

    struct DatabaseSpec
    {
        const char* path;
        const char* table;
        std::vector<Column> columns;
    };

    void check(sqlite3* db, int result)
    {
        if (result != SQLITE_OK) {
            throw std::runtime_error(db ? sqlite3_errmsg(db) : sqlite3_errstr(result));
        }
    }

    void execute(sqlite3* db, const std::string& sql)
    {
        check(db, sqlite3_exec(db, sql.c_str(), nullptr, nullptr, nullptr));
    }

    Statement prepare(sqlite3* db, const char* sql)
    {
        sqlite3_stmt* raw = nullptr;
        const int result = sqlite3_prepare_v2(db, sql, -1, &raw, nullptr);
        Statement statement(raw, &sqlite3_finalize);
        check(db, result);
        return statement;
    }

    std::string text(sqlite3_stmt* statement, int column)
    {
        const auto* value = sqlite3_column_text(statement, column);
        return value ? reinterpret_cast<const char*>(value) : "";
    }

    void create_table(sqlite3* db, const DatabaseSpec& spec)
    {
        // All SQL identifiers come from the fixed specifications below.
        std::string sql = "CREATE TABLE \"" + std::string(spec.table) + "\" (";
        for (std::size_t i = 0; i < spec.columns.size(); ++i) {
            const auto& column = spec.columns[i];
            if (i != 0) sql += ", ";
            sql += "\"" + std::string(column.name) + "\" " + column.type;
            if (column.primary_key) sql += " PRIMARY KEY NOT NULL";
            if (std::string(column.name) == "type") {
                sql += " CHECK (type IN ('illuminance', 'temperature'))";
            }
        }
        execute(db, sql + ");");
    }

    void validate_schema(sqlite3* db, const DatabaseSpec& spec)
    {
        auto table = prepare(db, "SELECT type FROM sqlite_schema WHERE name = ?1 COLLATE NOCASE;");
        check(db, sqlite3_bind_text(table.get(), 1, spec.table, -1, SQLITE_STATIC));
        int result = sqlite3_step(table.get());
        if (result != SQLITE_ROW && result != SQLITE_DONE) check(db, result);
        if (result != SQLITE_ROW || text(table.get(), 0) != "table") {
            throw std::runtime_error("Required table is missing: " + std::string(spec.table));
        }

        auto columns = prepare(db,
            "SELECT name, type, pk, hidden FROM pragma_table_xinfo(?1) ORDER BY cid;");
        check(db, sqlite3_bind_text(columns.get(), 1, spec.table, -1, SQLITE_STATIC));
        std::size_t index = 0;
        while ((result = sqlite3_step(columns.get())) == SQLITE_ROW) {
            if (index >= spec.columns.size()) {
                throw std::runtime_error("Unexpected column: " + text(columns.get(), 0));
            }
            const auto& expected = spec.columns[index];
            if (sqlite3_stricmp(text(columns.get(), 0).c_str(), expected.name) != 0 ||
                sqlite3_stricmp(text(columns.get(), 1).c_str(), expected.type) != 0 ||
                sqlite3_column_int(columns.get(), 2) != expected.primary_key ||
                sqlite3_column_int(columns.get(), 3) != 0) {
                throw std::runtime_error(
                    "Schema mismatch at column " + std::to_string(index + 1) +
                    ": expected " + expected.name + " " + expected.type +
                    (expected.primary_key ? " PRIMARY KEY" : "") +
                    ", got " + text(columns.get(), 0) + " " + text(columns.get(), 1));
            }
            ++index;
        }
        if (result != SQLITE_DONE) check(db, result);
        if (index != spec.columns.size()) 
        {
            throw std::runtime_error("Missing columns in table " + std::string(spec.table));
        }

        if (std::string(spec.table) == "telemetry") 
        {
            // Also check enum values in existing databases created without CHECK.
            auto invalid_type = prepare(db,
                "SELECT 1 FROM telemetry WHERE type IS NOT NULL "
                "AND type NOT IN ('illuminance', 'temperature') LIMIT 1;");
            result = sqlite3_step(invalid_type.get());
            if (result == SQLITE_ROW) {
                throw std::runtime_error("telemetry.type must be illuminance or temperature");
            }
            if (result != SQLITE_DONE) check(db, result);
        }
    }

    Database initialize_database(const DatabaseSpec& spec)
    {
        try {
            const fs::path path(spec.path);
            const bool existed = fs::exists(path);
            if (!existed) fs::create_directories(path.parent_path());

            sqlite3* raw = nullptr;
            const int result = sqlite3_open_v2
            (
                spec.path, &raw,
                SQLITE_OPEN_READWRITE | (existed ? 0 : SQLITE_OPEN_CREATE), 
                nullptr
            );
            Database db(raw, &sqlite3_close);
            check(db.get(), result);
            check(db.get(), sqlite3_busy_timeout(db.get(), 5000));

            execute(db.get(), "BEGIN;");
            if (!existed) create_table(db.get(), spec);
            validate_schema(db.get(), spec);
            execute(db.get(), "COMMIT;");

            std::cout << (existed ? "Validated" : "Created") << " SQLite "
                      << spec.table << ": " << spec.path << '\n';
            return db;
        }
        catch (const std::exception& error) {
            throw std::runtime_error(std::string(spec.path) + ": " + error.what());
        }
    }
}

int main()
{
    try 
    {
        const std::vector<Column> text_columns = 
        {
            {"id", "TEXT", 1}, {"text", "TEXT"}, {"timestamp", "INTEGER"}
        };
        const std::array<DatabaseSpec, 4> specs = {
        {
            {
                SQLITE_TELEMETRY_DB_PATH, 
                "telemetry", 
                {
                {"id", "TEXT", 1}, {"device_id", "TEXT"}, {"value", "REAL"},
                {"type", "TEXT"}, {"unit", "TEXT"}, {"serial", "INTEGER"},
                {"timestamp", "INTEGER"}
                }
            },
            {
                SQLITE_API_DB_PATH, 
                "api", 
                text_columns
            },
            {
                SQLITE_JOURNAL_DB_PATH, 
                "journal", 
                text_columns
            },
            {
                SQLITE_VOICE_DB_PATH, 
                "voice", 
                text_columns
            }
        }};

        std::set<fs::path> paths;
        for (const auto& spec : specs) 
        {
            if (!fs::path(spec.path).is_absolute()) 
            {
                throw std::runtime_error("Database path must be absolute: " + std::string(spec.path));
            }
            if (!paths.insert(fs::weakly_canonical(spec.path)).second) 
            {
                throw std::runtime_error("Database paths must be different: " + std::string(spec.path));
            }
        }

        std::vector<Database> databases;
        for (const auto& spec : specs) 
        {
            databases.push_back(initialize_database(spec));
        }

        std::signal(SIGINT, &request_stop);
        std::signal(SIGTERM, &request_stop);

        MqttSubscriber mqtt;
        std::cout << "Telemetry MQTT started in background" << std::endl;

        while (!stop_requested) 
        {
            mqtt.rethrow_if_failed();
            // TODO: работа с pipe (или mkfifo или UNIX socket) в основном потоке. Пока ожидаем завершения.
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
        }
        mqtt.rethrow_if_failed();

        return 0;
    }
    catch (const std::exception& error) 
    {
        std::cerr << "Telemetry failed: " << error.what() << '\n';
        return 1;
    }
}
