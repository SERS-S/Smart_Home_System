#pragma once

#include <sqlite3.h>
#include <nlohmann/json.hpp>
#include "sqlite_paths.h"

#include <array>
#include <cerrno>
#include <cmath>
#include <ctime>
#include <filesystem>
#include <iostream>
#include <limits>
#include <memory>
#include <set>
#include <stdexcept>
#include <string>
#include <vector>

namespace telemetry::db
{
    namespace fs = std::filesystem;
    using Database = std::unique_ptr<sqlite3, decltype(&sqlite3_close)>;
    using Statement = std::unique_ptr<sqlite3_stmt, decltype(&sqlite3_finalize)>;

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

    inline void check(sqlite3* db, int result)
    {
        if (result != SQLITE_OK) {
            throw std::runtime_error(db ? sqlite3_errmsg(db) : sqlite3_errstr(result));
        }
    }

    inline void execute(sqlite3* db, const std::string& sql)
    {
        check(db, sqlite3_exec(db, sql.c_str(), nullptr, nullptr, nullptr));
    }

    inline Statement prepare(sqlite3* db, const char* sql)
    {
        sqlite3_stmt* raw = nullptr;
        const int result = sqlite3_prepare_v2(db, sql, -1, &raw, nullptr);
        Statement statement(raw, &sqlite3_finalize);
        check(db, result);
        return statement;
    }

    inline std::string generate_uuid()
    {
        std::array<unsigned char, 16> bytes{};
        sqlite3_randomness(static_cast<int>(bytes.size()), bytes.data());
        bytes[6] = static_cast<unsigned char>((bytes[6] & 0x0f) | 0x40);
        bytes[8] = static_cast<unsigned char>((bytes[8] & 0x3f) | 0x80);

        constexpr char hex[] = "0123456789abcdef";
        std::string uuid;
        uuid.reserve(36);
        for (std::size_t i = 0; i < bytes.size(); ++i) {
            if (i == 4 || i == 6 || i == 8 || i == 10) uuid += '-';
            uuid += hex[bytes[i] >> 4];
            uuid += hex[bytes[i] & 0x0f];
        }
        return uuid;
    }

    inline sqlite3_int64 parse_utc_timestamp(const std::string& value)
    {
        // Sensor firmware sends UTC as YYYY-MM-DDTHH:MM:SSZ.
        if (value.size() != 20 || value[4] != '-' || value[7] != '-' ||
            value[10] != 'T' || value[13] != ':' || value[16] != ':' || value[19] != 'Z') {
            throw std::invalid_argument("timestamp must have format YYYY-MM-DDTHH:MM:SSZ");
        }
        for (std::size_t i = 0; i < value.size(); ++i) {
            if (i == 4 || i == 7 || i == 10 || i == 13 || i == 16 || i == 19) continue;
            if (value[i] < '0' || value[i] > '9') {
                throw std::invalid_argument("timestamp contains invalid digits");
            }
        }

        const int year = std::stoi(value.substr(0, 4));
        const int month = std::stoi(value.substr(5, 2));
        const int day = std::stoi(value.substr(8, 2));
        const int hour = std::stoi(value.substr(11, 2));
        const int minute = std::stoi(value.substr(14, 2));
        const int second = std::stoi(value.substr(17, 2));
        if (year == 0 || month < 1 || month > 12 || day < 1 || day > 31 ||
            hour > 23 || minute > 59 || second > 59) {
            throw std::invalid_argument("timestamp contains an invalid date or time");
        }

        std::tm utc{};
        utc.tm_year = year - 1900;
        utc.tm_mon = month - 1;
        utc.tm_mday = day;
        utc.tm_hour = hour;
        utc.tm_min = minute;
        utc.tm_sec = second;
        errno = 0;
        const std::time_t timestamp = ::timegm(&utc);
        // timegm normalizes dates such as February 30. Reject these explicitly.
        if ((timestamp == static_cast<std::time_t>(-1) && errno == EOVERFLOW) ||
            utc.tm_year != year - 1900 || utc.tm_mon != month - 1 ||
            utc.tm_mday != day || utc.tm_hour != hour || utc.tm_min != minute || utc.tm_sec != second) {
            throw std::invalid_argument("timestamp contains an invalid UTC date or is out of range");
        }
        return static_cast<sqlite3_int64>(timestamp);
    }

    inline bool save_telemetry_message(sqlite3* db, const std::string& topic, const std::string& payload)
    {
        const char* type = topic == "telemetry/home/light/data" ? "illuminance" : "temperature";
        const auto data = nlohmann::json::parse(payload, nullptr, false);
        const auto reject = [&topic](const char* reason) {
            std::cerr << "Ignoring " << topic << ": " << reason << std::endl;
            return false;
        };
        if (!data.is_object() ||
            !data.contains("device_id") || !data["device_id"].is_string() ||
            !data.contains(type) || !data[type].is_number() ||
            !data.contains("unit") || !data["unit"].is_string() ||
            !data.contains("serial") || !data["serial"].is_number_integer() ||
            !data.contains("timestamp") || !data["timestamp"].is_string()) {
            return reject("expected string device_id/unit/timestamp, numeric measurement and integer serial");
        }
        if 
        (
            data["serial"].is_number_unsigned() &&
            data["serial"].get<sqlite3_uint64>() > static_cast<sqlite3_uint64>(std::numeric_limits<sqlite3_int64>::max())
        ) 
        {
            return reject("serial is outside the SQLite INTEGER range");
        }
        const double value = data[type].get<double>();
        if (!std::isfinite(value)) return reject("measurement must be finite");

        sqlite3_int64 timestamp;
        try {
            timestamp = parse_utc_timestamp(data["timestamp"].get_ref<const std::string&>());
        }
        catch (const std::invalid_argument& error) {
            return reject(error.what());
        }

        auto statement = prepare(db,
            "INSERT INTO telemetry (id, device_id, value, type, unit, serial, timestamp) "
            "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7);");
        const auto bind_text = [&](int index, const std::string& value) {
            check(db, sqlite3_bind_text64(statement.get(), index, value.data(),
                static_cast<sqlite3_uint64>(value.size()), SQLITE_TRANSIENT, SQLITE_UTF8));
        };
        bind_text(1, generate_uuid());
        bind_text(2, data["device_id"].get_ref<const std::string&>());
        check(db, sqlite3_bind_double(statement.get(), 3, value));
        bind_text(4, type);
        bind_text(5, data["unit"].get_ref<const std::string&>());
        check(db, sqlite3_bind_int64(statement.get(), 6, data["serial"].get<sqlite3_int64>()));
        check(db, sqlite3_bind_int64(statement.get(), 7, timestamp));

        const int result = sqlite3_step(statement.get());
        if (result != SQLITE_DONE) check(db, result);
        return true;
    }

    inline void save_text_message(sqlite3* db, const char* insert_sql, const std::string& payload)
    {
        const std::string id = generate_uuid();
        // On the POSIX target, time() returns seconds since 1970-01-01 UTC
        const std::time_t timestamp = std::time(nullptr);
        if (timestamp == static_cast<std::time_t>(-1)) {
            throw std::runtime_error("Could not obtain system POSIX time");
        }

        auto statement = prepare(db, insert_sql);
        check(db, sqlite3_bind_text(statement.get(), 1, id.c_str(), -1, SQLITE_TRANSIENT));
        check(db, sqlite3_bind_text64(statement.get(), 2, payload.data(), static_cast<sqlite3_uint64>(payload.size()), SQLITE_TRANSIENT, SQLITE_UTF8));
        check(db, sqlite3_bind_int64(statement.get(), 3, static_cast<sqlite3_int64>(timestamp)));

        const int result = sqlite3_step(statement.get());
        if (result != SQLITE_DONE) check(db, result);
    }

    inline void save_api_message(sqlite3* db, const std::string& payload)
    {
        save_text_message(db, "INSERT INTO api (id, text, timestamp) VALUES (?1, ?2, ?3);", payload);
    }

    inline void save_voice_message(sqlite3* db, const std::string& message_text)
    {
        save_text_message(db, "INSERT INTO voice (id, text, timestamp) VALUES (?1, ?2, ?3);", message_text);
    }

    inline void save_journal_message(sqlite3* db, const std::string& payload)
    {
        save_text_message(db, "INSERT INTO journal (id, text, timestamp) VALUES (?1, ?2, ?3);", payload);
    }

    inline std::string text(sqlite3_stmt* statement, int column)
    {
        const auto* value = sqlite3_column_text(statement, column);
        return value ? reinterpret_cast<const char*>(value) : "";
    }

    inline void create_table(sqlite3* db, const DatabaseSpec& spec)
    {
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

    inline void validate_schema(sqlite3* db, const DatabaseSpec& spec)
    {
        auto table = prepare(db, "SELECT type FROM sqlite_schema WHERE name = ?1 COLLATE NOCASE;");
        check(db, sqlite3_bind_text(table.get(), 1, spec.table, -1, SQLITE_STATIC));
        int result = sqlite3_step(table.get());
        if (result != SQLITE_ROW && result != SQLITE_DONE) check(db, result);
        if (result != SQLITE_ROW || text(table.get(), 0) != "table") {
            throw std::runtime_error("Required table is missing: " + std::string(spec.table));
        }

        auto columns = prepare(db, "SELECT name, type, pk, hidden FROM pragma_table_xinfo(?1) ORDER BY cid;");
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

    inline Database initialize_database(const DatabaseSpec& spec)
    {
        try {
            const fs::path path(spec.path);
            const bool existed = fs::exists(path);
            if (!existed) fs::create_directories(path.parent_path());

            sqlite3* raw = nullptr;
            const int result = sqlite3_open_v2
            (
                spec.path, &raw,
                SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX | (existed ? 0 : SQLITE_OPEN_CREATE),
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

    inline std::vector<Database> initialize_databases()
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
        return databases;
    }
}
