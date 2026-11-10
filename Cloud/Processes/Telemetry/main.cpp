#include "db_func.hpp"

#include <mosquitto/libmosquitto.h>
#include <mosquitto/libcommon.h>
#include <nlohmann/json.hpp>

#include <array>
#include <chrono>
#include <csignal>
#include <exception>
#include <iostream>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <string>
#include <thread>

namespace db = telemetry::db;

namespace
{
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
        sqlite3* telemetry_database = nullptr;
        sqlite3* api_database = nullptr;
        sqlite3* voice_database = nullptr;
        sqlite3* journal_database = nullptr;
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
                auto& context = *static_cast<MqttContext*>(userdata);
                db::save_api_message(context.api_database, payload);
                
                std::cout << "Saved API weather message" << std::endl;
            } 
            else if (topic == "voice/home/data")
            {
                const auto data = nlohmann::json::parse(payload, nullptr, false);
                if (!data.is_object() || !data.contains("text") || !data["text"].is_string()) 
                {
                    std::cerr << "Ignoring voice/home/data: payload must be a JSON object with a string field text" << std::endl;
                    return;
                }

                auto& context = *static_cast<MqttContext*>(userdata);
                db::save_voice_message(context.voice_database, data["text"].get_ref<const std::string&>());

                std::cout << "Saved voice message" << std::endl;
            } 
            else if 
            (
                topic == "telemetry/home/light/data" ||
                topic == "telemetry/home/temperature/data"
            ) 
            {
                auto& context = *static_cast<MqttContext*>(userdata);
                if (db::save_telemetry_message(context.telemetry_database, topic, payload)) 
                {
                    std::cout << "Saved telemetry message [" << topic << "]" << std::endl;
                }
            }
            else if (topic == "journal/home/info")
            {
                auto& context = *static_cast<MqttContext*>(userdata);
                db::save_journal_message(context.journal_database, payload);

                std::cout << "Saved journal message" << std::endl;
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
        MqttSubscriber
        (
            sqlite3* telemetry_database, 
            sqlite3* api_database,
            sqlite3* voice_database, 
            sqlite3* journal_database
        )
        {
            if (!telemetry_database) throw std::runtime_error("Telemetry database connection is missing");
            if (!api_database) throw std::runtime_error("API database connection is missing");
            if (!voice_database) throw std::runtime_error("Voice database connection is missing");
            if (!journal_database) throw std::runtime_error("Journal database connection is missing");
            context.telemetry_database = telemetry_database;
            context.api_database = api_database;
            context.voice_database = voice_database;
            context.journal_database = journal_database;
            client.reset(mosquitto_new(nullptr, true, &context));
            if (!client) throw std::runtime_error("Could not create Telemetry MQTT client");

            mosquitto_message_callback_set(client.get(), &on_mqtt_message);
            mosquitto_connect_callback_set(client.get(),
                [](mosquitto* client, void* userdata, int result) noexcept 
                {
                    try 
                    {
                        if (result != 0) {
                            throw std::runtime_error("MQTT connection refused: " + std::to_string(result));
                        }
                        // Clean sessions need new subscriptions after every reconnect.
                        for (const char* topic : MQTT_TOPICS) {
                            mqtt_check(mosquitto_subscribe(client, nullptr, topic, 1));
                        }

                        std::cout << "Telemetry MQTT connected; subscriptions requested" << std::endl;
                    }
                    catch (...) 
                    {
                        mqtt_callback_failed(client, userdata);
                    }
                });
            mosquitto_subscribe_callback_set(client.get(),
                [](mosquitto* client, void* userdata, int, int count, const int* granted) noexcept 
                {
                    try 
                    {
                        for (int i = 0; i < count; ++i) {
                            if (granted[i] >= 128) {
                                throw std::runtime_error("MQTT broker rejected a Telemetry subscription");
                            }
                        }
                    }
                    catch (...) 
                    {
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
}

int main()
{
    try 
    {
        auto databases = db::initialize_databases();

        std::signal(SIGINT, &request_stop);
        std::signal(SIGTERM, &request_stop);

        MqttSubscriber mqtt
        (
            databases.at(0).get(), 
            databases.at(1).get(),
            databases.at(3).get(), 
            databases.at(2).get()
        );
        std::cout << "Telemetry MQTT started in background" << std::endl;

        while (!stop_requested) 
        {
            mqtt.rethrow_if_failed();
            // TODO: работа с pipe (или mkfifo или UNIX socket) в основном потоке

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
