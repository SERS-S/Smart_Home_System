#include <mosquitto/libmosquitto.h>
#include <mosquitto/libcommon.h>
#include "cjson/cJSON.h"

#include <array>
#include <cerrno>
#include <chrono>
#include <iostream>
#include <memory>
#include <stdexcept>
#include <string>
#include <sys/wait.h>
#include <unistd.h>

namespace 
{

    constexpr const char* WEATHER_URL =
        "https://api.open-meteo.com/v1/forecast"
        "?latitude=55.75&longitude=37.62"
        "&current=relative_humidity_2m,surface_pressure,weather_code,"
        "cloud_cover,visibility,wind_speed_10m,wind_direction_10m,wind_gusts_10m"
        "&daily=sunrise,sunset"
        "&wind_speed_unit=ms"
        "&timezone=GMT"
        "&forecast_days=1";
    constexpr const char* WEATHER_TOPIC = "api/home/weather";

    using Json = std::unique_ptr<cJSON, decltype(&cJSON_Delete)>;

    std::string fetch_weather()
    {
        int pipe_fd[2];
        if (pipe(pipe_fd) != 0) {
            throw std::runtime_error("Could not create pipe for curl");
        }
        const pid_t child = fork();
        if (child < 0) {
            close(pipe_fd[0]);
            close(pipe_fd[1]);
            throw std::runtime_error("Could not start curl");
        }
        if (child == 0) {
            close(pipe_fd[0]);
            if (dup2(pipe_fd[1], STDOUT_FILENO) < 0) _exit(127);
            close(pipe_fd[1]);
            execlp("curl", "curl", "--fail", "--silent", "--show-error",
                "--connect-timeout", "5", "--max-time", "15", WEATHER_URL,
                static_cast<char*>(nullptr));
            _exit(127);
        }

        close(pipe_fd[1]);
        std::string response;
        std::array<char, 4096> buffer;
        bool read_failed = false;
        for (;;) {
            const ssize_t bytes = read(pipe_fd[0], buffer.data(), buffer.size());
            if (bytes > 0) {
                response.append(buffer.data(), static_cast<std::size_t>(bytes));
            } else if (bytes == 0) {
                break;
            } else if (errno != EINTR) {
                read_failed = true;
                break;
            }
        }
        close(pipe_fd[0]);

        int status = 0;
        while (waitpid(child, &status, 0) < 0) {
            if (errno != EINTR) throw std::runtime_error("Could not wait for curl");
        }
        if (read_failed || !WIFEXITED(status) || WEXITSTATUS(status) != 0) {
            throw std::runtime_error("Weather request failed (curl exit status " +
                                    std::to_string(WIFEXITED(status) ? WEXITSTATUS(status) : -1) + ")");
        }
        return response;
    }

    const cJSON* object_field(const cJSON* object, const char* name)
    {
        const cJSON* field = cJSON_GetObjectItemCaseSensitive(object, name);
        if (!cJSON_IsObject(field)) {
            throw std::runtime_error(std::string("Missing object: ") + name);
        }
        return field;
    }

    double number_field(const cJSON* object, const char* name)
    {
        const cJSON* field = cJSON_GetObjectItemCaseSensitive(object, name);
        if (!cJSON_IsNumber(field)) {
            throw std::runtime_error(std::string("Missing number: ") + name);
        }
        return field->valuedouble;
    }

    std::string first_time(const cJSON* daily, const char* name)
    {
        const cJSON* times = cJSON_GetObjectItemCaseSensitive(daily, name);
        const cJSON* first = cJSON_GetArrayItem(times, 0);
        if (!cJSON_IsString(first)) {
            throw std::runtime_error(std::string("Missing daily time: ") + name);
        }
        // With timezone=GMT, Open-Meteo returns these ISO timestamps in UTC.
        return std::string(first->valuestring) + "Z";
    }

    const char* condition_from_code(int code)
    {
        if (code == 0 || code == 1) return "sunny";
        if (code == 2 || code == 3 || code == 45 || code == 48) return "cloudy";
        // The message schema has three states; precipitation maps to rainy.
        return "rainy";
    }

    std::string weather_message(const std::string& response)
    {
        Json source(cJSON_ParseWithLength(response.data(), response.size()), cJSON_Delete);
        if (!source) throw std::runtime_error("Invalid weather JSON");
        const cJSON* current = object_field(source.get(), "current");
        const cJSON* daily = object_field(source.get(), "daily");

        Json message(cJSON_CreateObject(), cJSON_Delete);
        if (!message) throw std::runtime_error("Could not allocate weather message");
        cJSON_AddNumberToObject(message.get(), "humidity", number_field(current, "relative_humidity_2m"));
        cJSON_AddNumberToObject(message.get(), "pressure", number_field(current, "surface_pressure"));
        cJSON_AddStringToObject(message.get(), "condition", condition_from_code(static_cast<int>(number_field(current, "weather_code"))));
        cJSON_AddNumberToObject(message.get(), "cloudiness", number_field(current, "cloud_cover"));
        cJSON_AddNumberToObject(message.get(), "visibility", number_field(current, "visibility"));

        cJSON* wind = cJSON_AddObjectToObject(message.get(), "wind");
        if (!wind) throw std::runtime_error("Could not allocate wind message");
        cJSON_AddNumberToObject(wind, "speed", number_field(current, "wind_speed_10m"));
        cJSON_AddNumberToObject(wind, "direction", number_field(current, "wind_direction_10m"));
        cJSON_AddNumberToObject(wind, "gust", number_field(current, "wind_gusts_10m"));

        cJSON* sun = cJSON_AddObjectToObject(message.get(), "sun");
        if (!sun) throw std::runtime_error("Could not allocate sun message");
        cJSON_AddStringToObject(sun, "sunrise", first_time(daily, "sunrise").c_str());
        cJSON_AddStringToObject(sun, "sunset", first_time(daily, "sunset").c_str());

        std::unique_ptr<char, decltype(&cJSON_free)> json(cJSON_PrintUnformatted(message.get()), cJSON_free);
        if (!json) throw std::runtime_error("Could not serialize weather message");
        return json.get();
    }

    struct PublishState {
        bool connected = false;
        bool published = false;
        int connect_result = -1;
        int message_id = -1;
    };

    void on_connect(mosquitto*, void* user_data, int result)
    {
        auto* state = static_cast<PublishState*>(user_data);
        state->connected = true;
        state->connect_result = result;
    }

    void on_publish(mosquitto*, void* user_data, int message_id)
    {
        auto* state = static_cast<PublishState*>(user_data);
        state->published = message_id == state->message_id;
    }

    void publish_weather(const std::string& payload)
    {
        if (mosquitto_lib_init() != MOSQ_ERR_SUCCESS) {
            throw std::runtime_error("Could not initialize MQTT client");
        }
        PublishState state;
        mosquitto* client = mosquitto_new(nullptr, true, &state);
        if (!client) {
            mosquitto_lib_cleanup();
            throw std::runtime_error("Could not create MQTT client");
        }
        mosquitto_connect_callback_set(client, on_connect);
        mosquitto_publish_callback_set(client, on_publish);

        auto wait_for = [&](bool& done) {
            const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(5);
            while (!done && std::chrono::steady_clock::now() < deadline) {
                const int result = mosquitto_loop(client, 1000, 1);
                if (result != MOSQ_ERR_SUCCESS) {
                    throw std::runtime_error(std::string("MQTT error: ") + mosquitto_strerror(result));
                }
            }
            if (!done) throw std::runtime_error("MQTT acknowledgement timed out");
        };

        try {
            std::string broker_host = MOSQUITTO_HOST;
            if (broker_host == "0.0.0.0") broker_host = "127.0.0.1";
            if (broker_host == "::") broker_host = "::1";
            const int result = mosquitto_connect(client, broker_host.c_str(), MOSQUITTO_PORT, 60);
            if (result != MOSQ_ERR_SUCCESS) {
                throw std::runtime_error(std::string("MQTT connection failed: ") + mosquitto_strerror(result));
            }
            wait_for(state.connected);
            if (state.connect_result != 0) 
            {
                throw std::runtime_error("MQTT broker refused the connection: " + std::to_string(state.connect_result));
            }

            const int publish_result = mosquitto_publish(client, &state.message_id,
                WEATHER_TOPIC, static_cast<int>(payload.size()), payload.data(), 1, true);
            if (publish_result != MOSQ_ERR_SUCCESS) {
                throw std::runtime_error(std::string("MQTT publish failed: ") + mosquitto_strerror(publish_result));
            }
            wait_for(state.published);
            mosquitto_disconnect(client);
            mosquitto_loop(client, 100, 1);
        } catch (...) {
            mosquitto_destroy(client);
            mosquitto_lib_cleanup();
            throw;
        }
        mosquitto_destroy(client);
        mosquitto_lib_cleanup();
    }

} // namespace

int main()
{
    try 
    {
        publish_weather(weather_message(fetch_weather()));

        std::cout << "Published weather to " << WEATHER_TOPIC << '\n';

        return 0;
    } 
    catch (const std::exception& error) 
    {
        std::cerr << error.what() << '\n';
        return 1;
    }
}
