#pragma once

#include <mosquitto/libmosquitto.h>
#include <mosquitto/libcommon.h>
#include "cjson/cJSON.h"

#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>

namespace voice
{
    constexpr const char* DATA_TOPIC = "voice/home/data";
    using Json = std::unique_ptr<cJSON, decltype(&cJSON_Delete)>;

    // Empty recognition results are not events. Always emit our own schema,
    // even if Vosk is configured to return word timings or confidence values.
    inline std::string recognition_payload(const std::string& result)
    {
        Json source(cJSON_ParseWithLength(result.data(), result.size()), &cJSON_Delete);
        if (!source) throw std::runtime_error("Invalid Vosk result JSON");
        const cJSON* text = cJSON_GetObjectItemCaseSensitive(source.get(), "text");
        if (!cJSON_IsString(text) || !text->valuestring) {
            throw std::runtime_error("Vosk result has no text string");
        }
        if (std::string(text->valuestring).find_first_not_of(" \t\r\n") == std::string::npos) {
            return {};
        }

        Json message(cJSON_CreateObject(), &cJSON_Delete);
        if (!message || !cJSON_AddStringToObject(message.get(), "text", text->valuestring)) {
            throw std::runtime_error("Could not allocate Voice message");
        }
        std::unique_ptr<char, decltype(&cJSON_free)> payload(
            cJSON_PrintUnformatted(message.get()), &cJSON_free);
        if (!payload) throw std::runtime_error("Could not serialize Voice message");
        return payload.get();
    }

    class MqttPublisher
    {
        struct Library
        {
            Library()
            {
                if (mosquitto_lib_init() != MOSQ_ERR_SUCCESS) {
                    throw std::runtime_error("Could not initialize MQTT library");
                }
            }
            ~Library() { mosquitto_lib_cleanup(); }
        } library_;

        std::unique_ptr<mosquitto, decltype(&mosquitto_destroy)> client_{
            nullptr, &mosquitto_destroy};

        static void check(int result, const char* operation)
        {
            if (result != MOSQ_ERR_SUCCESS) {
                throw std::runtime_error(std::string(operation) + ": " + mosquitto_strerror(result));
            }
        }

    public:
        MqttPublisher()
        {
            client_.reset(mosquitto_new(nullptr, true, nullptr));
            if (!client_) throw std::runtime_error("Could not create Voice MQTT client");

            mosquitto_connect_callback_set(client_.get(), [](mosquitto*, void*, int result) {
                if (result != 0) {
                    std::cerr << "Voice MQTT connection refused: " << result << std::endl;
                }
            });
            mosquitto_publish_callback_set(client_.get(), [](mosquitto*, void*, int) {
                std::cout << "Published voice text to " << DATA_TOPIC << std::endl;
            });
            check(mosquitto_reconnect_delay_set(client_.get(), 1, 30, true),
                "Could not configure MQTT reconnect");

            std::string host = MOSQUITTO_HOST;
            if (host == "0.0.0.0") host = "127.0.0.1";
            if (host == "::") host = "::1";
            check(mosquitto_connect(client_.get(), host.c_str(), MOSQUITTO_PORT, 60),
                "Voice MQTT connection failed");
            check(mosquitto_loop_start(client_.get()), "Could not start Voice MQTT loop");
        }

        ~MqttPublisher()
        {
            mosquitto_disconnect(client_.get());
            mosquitto_loop_stop(client_.get(), false);
        }

        void publish_result(const std::string& result)
        {
            const std::string payload = recognition_payload(result);
            if (payload.empty()) return;

            // Speech commands must not be replayed to newly connected clients.
            const int result_code = mosquitto_publish(client_.get(), nullptr, DATA_TOPIC,
                static_cast<int>(payload.size()), payload.data(), 1, false);
            // This client library queues QoS 1 messages before attempting
            // the write. Do not retry NO_CONN: reconnect will send that queue.
            if (result_code == MOSQ_ERR_NO_CONN) {
                std::cerr << "Voice message queued until MQTT reconnects" << std::endl;
                return;
            }
            check(result_code, "Could not publish Voice message");
        }
    };
}
